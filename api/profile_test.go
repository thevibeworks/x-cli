package api

import "testing"

func TestExtractProfileFull(t *testing.T) {
	raw := map[string]any{
		"data": map[string]any{
			"user": map[string]any{
				"result": map[string]any{
					"__typename":       "User",
					"rest_id":          "12345",
					"is_blue_verified": true,
					"legacy": map[string]any{
						"screen_name":     "jack",
						"name":            "Jack",
						"description":     "hello",
						"location":        "SF",
						"url":             "https://x.com",
						"followers_count": float64(1000),
						"friends_count":   float64(500),
						"statuses_count":  float64(42),
						"protected":       false,
						"created_at":      "Mon Jan 01 00:00:00 +0000 2007",
					},
				},
			},
		},
	}
	p := extractProfile(raw)
	if p == nil {
		t.Fatal("nil profile")
	}
	if p.RestID != "12345" {
		t.Errorf("RestID = %q", p.RestID)
	}
	if p.ID != "12345" {
		t.Errorf("ID should mirror RestID, got %q", p.ID)
	}
	if p.ScreenName != "jack" {
		t.Errorf("ScreenName = %q", p.ScreenName)
	}
	if p.Name != "Jack" {
		t.Errorf("Name = %q", p.Name)
	}
	if p.Description != "hello" {
		t.Errorf("Description = %q", p.Description)
	}
	if p.Followers != 1000 {
		t.Errorf("Followers = %d", p.Followers)
	}
	if p.Following != 500 {
		t.Errorf("Following = %d", p.Following)
	}
	if p.Tweets != 42 {
		t.Errorf("Tweets = %d", p.Tweets)
	}
	if !p.Verified {
		t.Error("Verified should be true")
	}
	if p.Protected {
		t.Error("Protected should be false")
	}
}

func TestExtractProfileEmptyResult(t *testing.T) {
	raw := map[string]any{
		"data": map[string]any{
			"user": map[string]any{"result": nil},
		},
	}
	if p := extractProfile(raw); p != nil {
		t.Errorf("want nil, got %+v", p)
	}
}

func TestExtractProfileNoLegacy(t *testing.T) {
	// Rare but possible: result with rest_id but no legacy block.
	raw := map[string]any{
		"data": map[string]any{
			"user": map[string]any{
				"result": map[string]any{
					"rest_id": "99",
				},
			},
		},
	}
	p := extractProfile(raw)
	if p == nil {
		t.Fatal("nil profile")
	}
	if p.RestID != "99" {
		t.Errorf("RestID = %q", p.RestID)
	}
	if p.ScreenName != "" {
		t.Errorf("ScreenName should be empty, got %q", p.ScreenName)
	}
}

func TestExtractProfileMalformed(t *testing.T) {
	cases := []map[string]any{
		{},
		{"data": "not-a-map"},
		{"data": map[string]any{"user": "not-a-map"}},
	}
	for i, raw := range cases {
		if p := extractProfile(raw); p != nil {
			t.Errorf("case %d: want nil, got %+v", i, p)
		}
	}
}

func TestGetStringFallback(t *testing.T) {
	if got := getString(map[string]any{"k": 123}, "k"); got != "" {
		t.Errorf("non-string should yield empty, got %q", got)
	}
	if got := getString(map[string]any{}, "missing"); got != "" {
		t.Errorf("missing key should yield empty, got %q", got)
	}
}

func TestGetIntVariants(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{float64(42), 42},
		{int(42), 42},
		{int64(42), 42},
		{"42", 42},  // X returns view counts as strings
		{"abc", 0},  // non-numeric string yields 0
		{nil, 0},
		{true, 0},
	}
	for _, tc := range cases {
		if got := getInt(map[string]any{"k": tc.in}, "k"); got != tc.want {
			t.Errorf("getInt(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
