package api

// Relationships scraping: followers, following, likers, retweeters.
// Mirrors XActions' src/scrapers/twitter/http/relationships.js.
//
// Stable rules:
//
//  1. Twitter packs user lists in the same TimelineAddEntries shape as
//     tweet timelines, but with `entryId` prefixed by `user-` and the
//     payload at `content.itemContent.user_results.result`.
//
//  2. Different endpoints route the timeline at different response paths:
//
//       Followers/Following → data.user.result.timeline.timeline.instructions
//       Retweeters          → data.retweeters_timeline.timeline.instructions
//       Favoriters          → data.favoriters_timeline.timeline.instructions
//
//  3. Pagination loops until either the bottom cursor is empty OR the
//     page returned no new users. Dedup by username via a Map (X
//     occasionally repeats users across pages).

import (
	"context"
	"strings"
)

// -----------------------------------------------------------------------------
// Types
// -----------------------------------------------------------------------------

// UserSummary is the projected view of a user as it appears in a follower
// or following list. It is a strict subset of the GraphQL response.
type UserSummary struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Name          string `json:"name"`
	Bio           string `json:"bio,omitempty"`
	Verified      bool   `json:"verified"`
	Protected     bool   `json:"protected,omitempty"`
	Avatar        string `json:"avatar,omitempty"`
	Followers     int    `json:"followers_count"`
	Following     int    `json:"following_count"`
	Tweets        int    `json:"tweets_count,omitempty"`
}

// -----------------------------------------------------------------------------
// Parsers
// -----------------------------------------------------------------------------

// ParseUserSummary projects a raw GraphQL user_results.result object.
// Returns nil for `UserUnavailable` typename.
//
// Defensive projection: reads `core.*` first (modern shape), falls back
// to `legacy.*` (XActions-vintage). Same rule as extractProfile.
func ParseUserSummary(raw any) *UserSummary {
	rm, ok := raw.(map[string]any)
	if !ok || rm == nil {
		return nil
	}
	if getString(rm, "__typename") == "UserUnavailable" {
		return nil
	}

	avatar := firstString(rm, "avatar/image_url", "legacy/profile_image_url_https")
	if avatar != "" {
		// X serves _normal (48x48) by default for legacy URLs;
		// bump to _400x400 for usable size. Modern `avatar.image_url`
		// already points at the larger asset, so the replace is a no-op.
		avatar = strings.Replace(avatar, "_normal", "_400x400", 1)
	}

	return &UserSummary{
		ID:        getString(rm, "rest_id"),
		Username:  firstString(rm, "core/screen_name", "legacy/screen_name"),
		Name:      firstString(rm, "core/name", "legacy/name"),
		Bio:       firstString(rm, "legacy/description"),
		Verified:  getBool(rm, "is_blue_verified") || firstBool(rm, "legacy/verified"),
		Protected: firstBool(rm, "privacy/protected", "legacy/protected"),
		Avatar:    avatar,
		Followers: firstInt(rm, "legacy/followers_count"),
		Following: firstInt(rm, "legacy/friends_count"),
		Tweets:    firstInt(rm, "legacy/statuses_count"),
	}
}

// ParseUserList walks a timeline `instructions` array, returning every
// `user-` entry projected to a UserSummary plus the bottom cursor.
func ParseUserList(insts []any) (users []*UserSummary, cursor string) {
	for _, inst := range insts {
		im, ok := inst.(map[string]any)
		if !ok {
			continue
		}
		typ := getString(im, "type")
		if typ == "" {
			typ = getString(im, "__typename")
		}
		switch typ {
		case "TimelineAddEntries":
			for _, e := range getSlice(im, "entries") {
				em, ok := e.(map[string]any)
				if !ok {
					continue
				}
				id := getString(em, "entryId")
				if strings.HasPrefix(id, "cursor-bottom-") {
					if v := extractCursorValue(em); v != "" {
						cursor = v
					}
					continue
				}
				if strings.HasPrefix(id, "cursor-top-") {
					continue
				}
				if !strings.HasPrefix(id, "user-") {
					continue
				}
				res := walkPathMap(em, "content", "itemContent", "user_results", "result")
				if u := ParseUserSummary(res); u != nil && u.Username != "" {
					users = append(users, u)
				}
			}
		case "TimelineAddToModule":
			for _, mi := range getSlice(im, "moduleItems") {
				mim, ok := mi.(map[string]any)
				if !ok {
					continue
				}
				res := walkPathMap(mim, "item", "itemContent", "user_results", "result")
				if u := ParseUserSummary(res); u != nil && u.Username != "" {
					users = append(users, u)
				}
			}
		}
	}
	return users, cursor
}

// -----------------------------------------------------------------------------
// Scraping API
// -----------------------------------------------------------------------------

// PageOptions configures any paginated user-list scrape.
type PageOptions struct {
	Limit  int
	Cursor string
	OnPage func(fetched, limit int)
}

// Followers scrapes the followers of `screenName`. Requires authentication.
func (c *Client) Followers(ctx context.Context, screenName string, opts PageOptions) ([]*UserSummary, error) {
	uid, err := c.resolveUserID(ctx, screenName)
	if err != nil {
		return nil, err
	}
	return c.scrapeUserList(ctx, "Followers",
		map[string]any{"userId": uid, "count": 20, "includePromotedContent": false},
		opts,
		"data", "user", "result", "timeline", "timeline", "instructions")
}

// Following scrapes the accounts `screenName` follows. Requires authentication.
func (c *Client) Following(ctx context.Context, screenName string, opts PageOptions) ([]*UserSummary, error) {
	uid, err := c.resolveUserID(ctx, screenName)
	if err != nil {
		return nil, err
	}
	return c.scrapeUserList(ctx, "Following",
		map[string]any{"userId": uid, "count": 20, "includePromotedContent": false},
		opts,
		"data", "user", "result", "timeline", "timeline", "instructions")
}

// Likers scrapes the users who liked a tweet. Requires authentication.
func (c *Client) Likers(ctx context.Context, tweetID string, opts PageOptions) ([]*UserSummary, error) {
	return c.scrapeUserList(ctx, "Favoriters",
		map[string]any{"tweetId": tweetID, "count": 20, "includePromotedContent": false},
		opts,
		"data", "favoriters_timeline", "timeline", "instructions")
}

// Retweeters scrapes the users who retweeted a tweet. Requires authentication.
func (c *Client) Retweeters(ctx context.Context, tweetID string, opts PageOptions) ([]*UserSummary, error) {
	return c.scrapeUserList(ctx, "Retweeters",
		map[string]any{"tweetId": tweetID, "count": 20, "includePromotedContent": false},
		opts,
		"data", "retweeters_timeline", "timeline", "instructions")
}

// scrapeUserList implements the generic paginated user-list pattern:
// fetch → parse → dedup → progress callback → cursor advance.
func (c *Client) scrapeUserList(
	ctx context.Context,
	endpointName string,
	baseVars map[string]any,
	opts PageOptions,
	pathToInstructions ...string,
) ([]*UserSummary, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 1000
	}
	cursor := opts.Cursor

	seen := make(map[string]struct{}, limit)
	out := make([]*UserSummary, 0, limit)

	for len(out) < limit {
		vars := copyMap(baseVars)
		if cursor != "" {
			vars["cursor"] = cursor
		}
		var raw map[string]any
		if err := c.GraphQL(ctx, endpointName, vars, &raw); err != nil {
			return nil, err
		}
		insts := walkPathSlice(raw, pathToInstructions...)
		users, bottom := ParseUserList(insts)

		newCount := 0
		for _, u := range users {
			if len(out) >= limit {
				break
			}
			if _, dup := seen[u.Username]; dup {
				continue
			}
			seen[u.Username] = struct{}{}
			out = append(out, u)
			newCount++
		}
		if opts.OnPage != nil {
			opts.OnPage(len(out), limit)
		}
		if bottom == "" || newCount == 0 {
			break
		}
		cursor = bottom
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
