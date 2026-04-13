package api

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCookieString(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]string
	}{
		{"empty", "", map[string]string{}},
		{"single", "auth_token=abc", map[string]string{"auth_token": "abc"}},
		{"spaced", "auth_token=abc; ct0=def", map[string]string{"auth_token": "abc", "ct0": "def"}},
		{"no-space", "auth_token=abc;ct0=def", map[string]string{"auth_token": "abc", "ct0": "def"}},
		{"url-encoded-value", "twid=u%3D123", map[string]string{"twid": "u%3D123"}},
		{"equals-in-value", "k=a=b", map[string]string{"k": "a=b"}},
		{"ignore-empty-name", "=nothing; ct0=def", map[string]string{"ct0": "def"}},
		{"drop-empty-value", "auth_token=; ct0=def", map[string]string{"ct0": "def"}},
		{"trim-whitespace", "  auth_token =  abc  ; ct0=def", map[string]string{"auth_token": "abc", "ct0": "def"}},
		{"trailing-semicolon", "auth_token=abc;", map[string]string{"auth_token": "abc"}},
		{"key-only", "auth_token", map[string]string{}},
		{"realistic", "auth_token=1a2b3c; ct0=deadbeef; twid=u%3D999; guest_id=v1%3A123", map[string]string{
			"auth_token": "1a2b3c",
			"ct0":        "deadbeef",
			"twid":       "u%3D999",
			"guest_id":   "v1%3A123",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseCookieString(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseCookieString(%q):\n  got  %v\n  want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestRequireAuthCookies(t *testing.T) {
	cases := []struct {
		name    string
		in      map[string]string
		wantErr string
	}{
		{"empty-map", map[string]string{}, "auth_token"},
		{"only-auth-token", map[string]string{"auth_token": "x"}, "ct0"},
		{"only-ct0", map[string]string{"ct0": "x"}, "auth_token"},
		{"both-present", map[string]string{"auth_token": "x", "ct0": "y"}, ""},
		{"empty-auth-token", map[string]string{"auth_token": "", "ct0": "y"}, "auth_token"},
		{"empty-ct0", map[string]string{"auth_token": "x", "ct0": ""}, "ct0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RequireAuthCookies(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantErr)
			}
			if _, ok := err.(*AuthError); !ok {
				t.Fatalf("want *AuthError, got %T", err)
			}
		})
	}
}
