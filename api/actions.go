package api

// Mutating actions: follow / unfollow. Mirrors the GraphQL/REST mutation
// helpers in XActions' src/scrapers/twitter/http/engagement.js, with one
// important addition: x-cli inspects the response body for X's "errors"
// envelope and treats idempotent failures (already following, etc.) as
// success. Without that, a retry after a partial success looks like a
// failure and the caller can't tell the difference.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// FollowUser follows a user by their numeric `rest_id`. Routes through
// the REST `friendshipsCreate` endpoint, which is what real browsers use
// for follow actions. Throttle-aware via Client.REST.
func (c *Client) FollowUser(ctx context.Context, userID string) error {
	if userID == "" {
		return &APIError{Endpoint: "friendshipsCreate", Status: 0, Body: "empty user id"}
	}
	form := url.Values{}
	form.Set("user_id", userID)
	form.Set("include_profile_interstitial_type", "1")
	form.Set("skip_status", "true")
	return c.restMutationCheckErrors(ctx, "friendshipsCreate", form)
}

// UnfollowUser unfollows a user by their numeric `rest_id`.
func (c *Client) UnfollowUser(ctx context.Context, userID string) error {
	if userID == "" {
		return &APIError{Endpoint: "friendshipsDestroy", Status: 0, Body: "empty user id"}
	}
	form := url.Values{}
	form.Set("user_id", userID)
	form.Set("include_profile_interstitial_type", "1")
	form.Set("skip_status", "true")
	return c.restMutationCheckErrors(ctx, "friendshipsDestroy", form)
}

// FollowByUsername resolves a screen name to a user ID and follows them.
func (c *Client) FollowByUsername(ctx context.Context, screenName string) error {
	uid, err := c.resolveUserID(ctx, strings.TrimPrefix(screenName, "@"))
	if err != nil {
		return err
	}
	return c.FollowUser(ctx, uid)
}

// LikeTweet favorites a tweet via the FavoriteTweet GraphQL mutation.
// Idempotent: returns nil if X says "you have already favorited".
func (c *Client) LikeTweet(ctx context.Context, tweetID string) error {
	return c.graphqlMutation(ctx, "FavoriteTweet", map[string]any{"tweet_id": tweetID})
}

// UnlikeTweet unfavorites a tweet via UnfavoriteTweet.
func (c *Client) UnlikeTweet(ctx context.Context, tweetID string) error {
	return c.graphqlMutation(ctx, "UnfavoriteTweet", map[string]any{"tweet_id": tweetID})
}

// BookmarkTweet adds a bookmark via CreateBookmark.
func (c *Client) BookmarkTweet(ctx context.Context, tweetID string) error {
	return c.graphqlMutation(ctx, "CreateBookmark", map[string]any{"tweet_id": tweetID})
}

// UnbookmarkTweet removes a bookmark via DeleteBookmark.
func (c *Client) UnbookmarkTweet(ctx context.Context, tweetID string) error {
	return c.graphqlMutation(ctx, "DeleteBookmark", map[string]any{"tweet_id": tweetID})
}

// graphqlMutation runs a GraphQL mutation by name, parses the response
// envelope, and routes idempotent successes / rate-limits / not-found
// the same way classifyMutationErrors does for REST.
//
// Throttle accounting: GraphQL mutations go through Client.GraphQL
// which already runs through the read token bucket. This is a
// deliberate compromise — the dedicated mutation budget is tied to
// the REST friendshipsCreate path. For per-op rate limits on like /
// retweet / bookmark X enforces its own server-side caps; we observe
// 429s via the throttle and back off.
func (c *Client) graphqlMutation(ctx context.Context, name string, vars map[string]any) error {
	var raw map[string]any
	if err := c.GraphQL(ctx, name, vars, &raw); err != nil {
		return err
	}
	return classifyMutationErrors(name, raw)
}

// restMutationCheckErrors wraps Client.REST and inspects the decoded body
// for X's `errors[]` envelope. The envelope dispatch is:
//
//   - "already following" / "you have already" / variants → silent success
//   - "rate limit" / "to protect our users from spam"     → RateLimitError
//   - "cannot find specified user" / "user not found"     → NotFoundError
//   - "suspended"                                         → APIError (final)
//   - anything else                                       → APIError
//
// Throttle accounting and exponential backoff are handled inside Client.REST.
// This wrapper only handles the message-level dispatch.
func (c *Client) restMutationCheckErrors(ctx context.Context, endpointName string, form url.Values) error {
	var body map[string]any
	if err := c.REST(ctx, endpointName, form, &body); err != nil {
		return err
	}
	return classifyMutationErrors(endpointName, body)
}

// classifyMutationErrors dispatches the `errors[]` envelope. Exported as
// an internal helper for use by tests and future GraphQL mutation paths.
func classifyMutationErrors(endpointName string, body map[string]any) error {
	errs := getSlice(body, "errors")
	if len(errs) == 0 {
		return nil
	}
	for _, e := range errs {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		msg := strings.ToLower(getString(em, "message"))

		// Idempotent — already in the desired state.
		if strings.Contains(msg, "already favorited") ||
			strings.Contains(msg, "already retweeted") ||
			strings.Contains(msg, "already bookmarked") ||
			strings.Contains(msg, "you have already") ||
			strings.Contains(msg, "already followed") ||
			strings.Contains(msg, "already following") ||
			strings.Contains(msg, "not found in list of retweets") {
			return nil
		}

		// Rate limit (server-side message variant — Throttle.Observe handles
		// HTTP 429 separately).
		if strings.Contains(msg, "rate limit") ||
			strings.Contains(msg, "to protect our users from spam") ||
			strings.Contains(msg, "too many requests") {
			return &RateLimitError{Endpoint: endpointName}
		}

		// Not found.
		if strings.Contains(msg, "cannot find specified user") ||
			strings.Contains(msg, "user not found") ||
			strings.Contains(msg, "user has been suspended") {
			return &NotFoundError{Endpoint: endpointName}
		}

		// Suspended (the actor — i.e. our own session).
		if strings.Contains(msg, "your account is suspended") ||
			strings.Contains(msg, "this account is suspended") {
			return &APIError{Endpoint: endpointName, Status: 0, Body: getString(em, "message")}
		}
	}

	// Unrecognized error envelope — surface the raw messages.
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		if em, ok := e.(map[string]any); ok {
			if msg := getString(em, "message"); msg != "" {
				parts = append(parts, msg)
			}
		}
	}
	if len(parts) == 0 {
		raw, _ := json.Marshal(body)
		return &APIError{Endpoint: endpointName, Status: 0, Body: string(raw)}
	}
	return &APIError{Endpoint: endpointName, Status: 0, Body: fmt.Sprintf("graphql errors: %s", strings.Join(parts, "; "))}
}
