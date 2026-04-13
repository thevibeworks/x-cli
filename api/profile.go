package api

import "context"

// Profile is a thin, stable view over X's UserByScreenName / UserByRestId
// GraphQL results. The raw shape is deeply nested and changes often; we only
// project the fields we actually use.
type Profile struct {
	ID          string `json:"id"`
	RestID      string `json:"rest_id"`
	ScreenName  string `json:"screen_name"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Location    string `json:"location"`
	URL         string `json:"url"`
	Avatar      string `json:"avatar,omitempty"`
	Followers   int    `json:"followers_count"`
	Following   int    `json:"friends_count"`
	Tweets      int    `json:"statuses_count"`
	Verified    bool   `json:"verified"`
	Protected   bool   `json:"protected"`
	CreatedAt   string `json:"created_at"`
}

// GetProfile resolves a screen name to a Profile via UserByScreenName.
func (c *Client) GetProfile(ctx context.Context, screenName string) (*Profile, error) {
	vars := map[string]any{
		"screen_name":            screenName,
		"withGrokTranslatedBio":  false,
	}
	var raw map[string]any
	if err := c.GraphQL(ctx, "UserByScreenName", vars, &raw); err != nil {
		return nil, err
	}
	p := extractProfile(raw)
	if p == nil {
		return nil, &NotFoundError{Endpoint: "user:" + screenName}
	}
	return p, nil
}

// GetProfileByID resolves a numeric user ID to a Profile via UserByRestId.
// Used by the auth-import liveness check (twid → rest_id → profile).
func (c *Client) GetProfileByID(ctx context.Context, userID string) (*Profile, error) {
	vars := map[string]any{"userId": userID}
	var raw map[string]any
	if err := c.GraphQL(ctx, "UserByRestId", vars, &raw); err != nil {
		return nil, err
	}
	p := extractProfile(raw)
	if p == nil {
		return nil, &NotFoundError{Endpoint: "user:" + userID}
	}
	return p, nil
}

// extractProfile walks the UserByScreenName response shape.
//
// X has shipped at least two shapes for this response. The defensive
// projection reads from `core.*` (modern) first, then falls back to
// `legacy.*` (XActions-vintage), so x-cli works across the rotation
// without requiring an endpoints.yaml hot-patch.
//
// Shape (trimmed, modern):
//   data.user.result
//     ├── __typename     "User" | "UserUnavailable"
//     ├── rest_id
//     ├── core           { screen_name, name, created_at }
//     ├── avatar         { image_url }
//     ├── location       { ... }
//     ├── legacy         { description, followers_count, friends_count,
//     │                    statuses_count, url, protected, ... }
//     └── is_blue_verified
func extractProfile(raw map[string]any) *Profile {
	result := walkPathMap(raw, "data", "user", "result")
	if result == nil {
		return nil
	}
	if getString(result, "__typename") == "UserUnavailable" {
		return nil
	}

	p := &Profile{
		RestID: getString(result, "rest_id"),
	}
	p.ID = p.RestID
	p.Verified = getBool(result, "is_blue_verified")

	// New shape: core.screen_name / core.name / core.created_at
	// Old shape: legacy.screen_name / legacy.name / legacy.created_at
	p.ScreenName = firstString(result, "core/screen_name", "legacy/screen_name")
	p.Name = firstString(result, "core/name", "legacy/name")
	p.CreatedAt = firstString(result, "core/created_at", "legacy/created_at")

	// New shape: avatar.image_url
	// Old shape: legacy.profile_image_url_https
	p.Avatar = firstString(result, "avatar/image_url", "legacy/profile_image_url_https")

	// Legacy block fields (most are still here in the modern shape).
	p.Description = firstString(result, "legacy/description")
	p.Location = firstString(result, "location/location", "legacy/location")
	p.URL = firstString(result, "legacy/url")
	p.Followers = firstInt(result, "legacy/followers_count")
	p.Following = firstInt(result, "legacy/friends_count")
	p.Tweets = firstInt(result, "legacy/statuses_count")
	p.Protected = firstBool(result, "privacy/protected", "legacy/protected")

	return p
}

