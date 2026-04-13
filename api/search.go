package api

// Search: tweets and users via the SearchTimeline GraphQL endpoint.
// Mirrors XActions' src/scrapers/twitter/http/search.js.
//
// SearchTimeline takes a `product` flag that switches the response shape:
//
//   product: "Latest"  → tweet timeline (sorted newest first)
//   product: "Top"     → tweet timeline (algorithm-ranked)
//   product: "People"  → user list
//   product: "Photos"  → tweet timeline (media only)
//   product: "Videos"  → tweet timeline (videos only)
//
// All responses live at:
//   data.search_by_raw_query.search_timeline.timeline.instructions

import (
	"context"
	"strconv"
	"strings"
)

// SearchOptions configures a tweet search.
type SearchOptions struct {
	Limit       int
	Cursor      string
	Product     string // "Latest" | "Top" | "Photos" | "Videos"
	OnPage      func(fetched, limit int)

	// Inline filters merged into the raw query string.
	Since       string // YYYY-MM-DD
	Until       string // YYYY-MM-DD
	From        string
	To          string
	MinLikes    int
	MinRetweets int
	MinReplies  int
	Lang        string
	Filter      string // "links" | "images" | "videos" | "media" | "native_video"
	Exclude     string // "retweets" | "replies"
}

// SearchPosts runs a tweet search via SearchTimeline.
func (c *Client) SearchPosts(ctx context.Context, query string, opts SearchOptions) ([]*Tweet, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	product := opts.Product
	if product == "" {
		product = "Latest"
	}
	rawQuery := buildAdvancedQuery(query, opts)

	cursor := opts.Cursor
	out := make([]*Tweet, 0, limit)

	for len(out) < limit {
		vars := map[string]any{
			"rawQuery":    rawQuery,
			"count":       20,
			"querySource": "typed_query",
			"product":     product,
		}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		var raw map[string]any
		if err := c.GraphQL(ctx, "SearchTimeline", vars, &raw); err != nil {
			return nil, err
		}
		insts := walkPathSlice(raw,
			"data", "search_by_raw_query", "search_timeline", "timeline", "instructions")
		tweets, bottom := ParseTimelineInstructions(insts)
		for _, t := range tweets {
			if len(out) >= limit {
				break
			}
			out = append(out, t)
		}
		if opts.OnPage != nil {
			opts.OnPage(len(out), limit)
		}
		if bottom == "" || len(tweets) == 0 {
			break
		}
		cursor = bottom
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SearchUsers runs a user search via SearchTimeline with product=People.
//
// User search results carry `user_results` instead of `tweet_results` in
// each entry, so we use a dedicated parser path.
func (c *Client) SearchUsers(ctx context.Context, query string, opts SearchOptions) ([]*UserSummary, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	cursor := opts.Cursor
	out := make([]*UserSummary, 0, limit)
	seen := make(map[string]struct{}, limit)

	for len(out) < limit {
		vars := map[string]any{
			"rawQuery":    query,
			"count":       20,
			"querySource": "typed_query",
			"product":     "People",
		}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		var raw map[string]any
		if err := c.GraphQL(ctx, "SearchTimeline", vars, &raw); err != nil {
			return nil, err
		}
		insts := walkPathSlice(raw,
			"data", "search_by_raw_query", "search_timeline", "timeline", "instructions")
		users, bottom := parseSearchUserInstructions(insts)
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

// parseSearchUserInstructions extracts users from a SearchTimeline (People).
// Entries point at `content.itemContent.user_results.result`.
func parseSearchUserInstructions(insts []any) (users []*UserSummary, cursor string) {
	for _, inst := range insts {
		im, ok := inst.(map[string]any)
		if !ok || getString(im, "type") != "TimelineAddEntries" {
			continue
		}
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
			res := walkPathMap(em, "content", "itemContent", "user_results", "result")
			if u := ParseUserSummary(res); u != nil && u.Username != "" {
				users = append(users, u)
			}
		}
	}
	return users, cursor
}

// buildAdvancedQuery merges inline SearchOptions filters into the raw query.
// This is the same composition XActions uses; the syntax is X's documented
// advanced search grammar.
func buildAdvancedQuery(query string, opts SearchOptions) string {
	parts := []string{}
	if query != "" {
		parts = append(parts, query)
	}
	if opts.From != "" {
		parts = append(parts, "from:"+opts.From)
	}
	if opts.To != "" {
		parts = append(parts, "to:"+opts.To)
	}
	if opts.Since != "" {
		parts = append(parts, "since:"+opts.Since)
	}
	if opts.Until != "" {
		parts = append(parts, "until:"+opts.Until)
	}
	if opts.MinLikes > 0 {
		parts = append(parts, "min_faves:"+strconv.Itoa(opts.MinLikes))
	}
	if opts.MinRetweets > 0 {
		parts = append(parts, "min_retweets:"+strconv.Itoa(opts.MinRetweets))
	}
	if opts.MinReplies > 0 {
		parts = append(parts, "min_replies:"+strconv.Itoa(opts.MinReplies))
	}
	if opts.Lang != "" {
		parts = append(parts, "lang:"+opts.Lang)
	}
	if opts.Filter != "" {
		parts = append(parts, "filter:"+opts.Filter)
	}
	if opts.Exclude != "" {
		parts = append(parts, "-filter:"+opts.Exclude)
	}
	return strings.Join(parts, " ")
}

// BuildAdvancedQuery is the exported form for callers that want to inspect
// the composed query (e.g. echo it in `--verbose` mode).
func BuildAdvancedQuery(query string, opts SearchOptions) string {
	return buildAdvancedQuery(query, opts)
}

