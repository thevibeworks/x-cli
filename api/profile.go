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
	Followers   int    `json:"followers_count"`
	Following   int    `json:"friends_count"`
	Tweets      int    `json:"statuses_count"`
	Verified    bool   `json:"verified"`
	Protected   bool   `json:"protected"`
	CreatedAt   string `json:"created_at"`
}

// GetProfile resolves a screen name to a Profile.
func (c *Client) GetProfile(ctx context.Context, screenName string) (*Profile, error) {
	vars := map[string]any{
		"screen_name":              screenName,
		"withSafetyModeUserFields": true,
	}

	var raw map[string]any
	if err := c.GraphQL(ctx, "UserByScreenName", vars, &raw); err != nil {
		return nil, err
	}

	return extractProfile(raw), nil
}

// extractProfile walks the UserByScreenName response shape.
//
// Shape (trimmed):
//   data.user.result
//     ├── __typename     "User" | "UserUnavailable"
//     ├── rest_id
//     ├── legacy         { screen_name, name, description, ... }
//     └── is_blue_verified
func extractProfile(raw map[string]any) *Profile {
	data, _ := raw["data"].(map[string]any)
	user, _ := data["user"].(map[string]any)
	result, _ := user["result"].(map[string]any)
	if result == nil {
		return nil
	}

	p := &Profile{}
	if v, ok := result["rest_id"].(string); ok {
		p.RestID = v
		p.ID = v
	}
	if v, ok := result["is_blue_verified"].(bool); ok {
		p.Verified = v
	}

	legacy, _ := result["legacy"].(map[string]any)
	if legacy != nil {
		p.ScreenName = getString(legacy, "screen_name")
		p.Name = getString(legacy, "name")
		p.Description = getString(legacy, "description")
		p.Location = getString(legacy, "location")
		p.URL = getString(legacy, "url")
		p.CreatedAt = getString(legacy, "created_at")
		p.Followers = getInt(legacy, "followers_count")
		p.Following = getInt(legacy, "friends_count")
		p.Tweets = getInt(legacy, "statuses_count")
		if v, ok := legacy["protected"].(bool); ok {
			p.Protected = v
		}
	}
	return p
}

func getString(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

func getInt(m map[string]any, k string) int {
	switch x := m[k].(type) {
	case float64:
		return int(x)
	case int:
		return x
	}
	return 0
}
