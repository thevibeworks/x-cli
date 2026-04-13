package api

// Shared projection helpers for walking decoded GraphQL responses. Every
// domain file (tweets.go, relationships.go, search.go, profile.go) uses
// these to extract typed fields from `map[string]any` without the caller
// having to spell out type assertions.
//
// All helpers return a zero value on type mismatch, missing key, or nil
// receiver. Panic-free by design — X's response shapes vary enough that
// defensive extraction is always the right call.

// getMap returns m[key] as map[string]any, or nil if missing/wrong type.
func getMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

// getSlice returns m[key] as []any, or nil if missing/wrong type.
func getSlice(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	v, _ := m[key].([]any)
	return v
}

// getString returns m[key] as string, or "" if missing/wrong type.
func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// getBool returns m[key] as bool, or false if missing/wrong type.
func getBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	b, _ := m[key].(bool)
	return b
}

// getInt returns m[key] as int, converting float64 from encoding/json
// and parsing string-encoded numbers (X returns some counts as strings,
// notably tweet view counts). Returns 0 on missing/wrong type.
func getInt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch x := m[key].(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case string:
		// X's `views.count` is a string-encoded integer.
		var n int
		for _, r := range x {
			if r < '0' || r > '9' {
				return 0
			}
			n = n*10 + int(r-'0')
		}
		return n
	}
	return 0
}

// getInt64 is the same as getInt but preserves int64 precision for
// Twitter's 19-digit snowflake IDs when they are decoded as numbers.
// Prefer reading IDs as strings (Twitter's id_str / rest_id) — this is a
// fallback only.
func getInt64(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch x := m[key].(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	}
	return 0
}

// walkPath navigates nested `map[string]any` by a list of keys. Returns
// nil if any intermediate key is missing or wrong type.
//
// Example:
//
//	walkPath(resp, "data", "user", "result", "timeline_v2", "timeline", "instructions")
func walkPath(root any, keys ...string) any {
	cur := root
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

// walkPathSlice returns walkPath coerced to []any.
func walkPathSlice(root any, keys ...string) []any {
	v, _ := walkPath(root, keys...).([]any)
	return v
}

// walkPathMap returns walkPath coerced to map[string]any.
func walkPathMap(root any, keys ...string) map[string]any {
	v, _ := walkPath(root, keys...).(map[string]any)
	return v
}

// copyMap returns a shallow copy of m. Used to clone the base variables
// for a paginated GraphQL call so the original map is not mutated when
// the caller adds a `cursor` key.
func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// firstString returns the first non-empty string at any of the given
// dot-paths in the root. This is the defensive projection helper used
// across the parsers — Twitter has shipped at least two response shapes
// for the same field (e.g., user.screen_name now lives at `core.screen_name`
// but used to live at `legacy.screen_name`). Reading both keeps x-cli
// working across the rotation.
//
// Each `paths` argument is a slash-delimited path like
// "core/screen_name" or "legacy/profile_image_url_https".
func firstString(root map[string]any, paths ...string) string {
	for _, p := range paths {
		keys := splitPath(p)
		v := walkPath(root, keys...)
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// firstInt is the int counterpart of firstString.
func firstInt(root map[string]any, paths ...string) int {
	for _, p := range paths {
		keys := splitPath(p)
		v := walkPath(root, keys...)
		switch x := v.(type) {
		case float64:
			return int(x)
		case int:
			return x
		case int64:
			return int(x)
		case string:
			n := 0
			ok := len(x) > 0
			for _, r := range x {
				if r < '0' || r > '9' {
					ok = false
					break
				}
				n = n*10 + int(r-'0')
			}
			if ok {
				return n
			}
		}
	}
	return 0
}

// firstBool returns the first true value at any of the given paths.
func firstBool(root map[string]any, paths ...string) bool {
	for _, p := range paths {
		keys := splitPath(p)
		if v, ok := walkPath(root, keys...).(bool); ok && v {
			return true
		}
	}
	return false
}

func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	out = append(out, p[start:])
	return out
}
