package browsercookies

import (
	"context"
	"strings"
	"testing"
)

func TestFormatCookieHeaderAll(t *testing.T) {
	in := map[string]string{
		"auth_token": "AT",
		"ct0":        "CSRF",
		"twid":       "u%3D42",
	}
	got := FormatCookieHeader(in, nil)
	for _, want := range []string{"auth_token=AT", "ct0=CSRF", "twid=u%3D42"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestFormatCookieHeaderSubset(t *testing.T) {
	in := map[string]string{
		"auth_token":   "AT",
		"ct0":          "CSRF",
		"marketing_id": "spam",
	}
	got := FormatCookieHeader(in, []string{"auth_token", "ct0"})
	if strings.Contains(got, "marketing_id") {
		t.Errorf("subset should drop marketing_id, got %q", got)
	}
	for _, want := range []string{"auth_token=AT", "ct0=CSRF"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestFormatCookieHeaderDropsEmpty(t *testing.T) {
	in := map[string]string{
		"auth_token": "AT",
		"empty":      "",
	}
	got := FormatCookieHeader(in, nil)
	if strings.Contains(got, "empty=") {
		t.Errorf("should drop empty values, got %q", got)
	}
}

func TestFormatCookieHeaderNilSafe(t *testing.T) {
	if FormatCookieHeader(nil, nil) != "" {
		t.Error("nil map should yield empty string")
	}
}

// TestLoadNoBrowsersHere exercises the error path on a machine that
// has no browser cookie stores at the expected paths (CI runners and
// Linux containers, typically). The exact error message depends on
// the host; we just assert it returns an error rather than panicking.
func TestLoadNoBrowsersHere(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5_000_000_000)
	defer cancel()
	_, err := Load(ctx, "chrome", "x.com")
	if err == nil {
		// Some CI runners actually have a Chrome installed (Ubuntu image
		// pulls google-chrome-stable). If so, we still expect zero cookies
		// for x.com because no one is logged in. Either path is fine.
		t.Skip("environment unexpectedly has chrome cookies; skipping")
	}
}

func TestLoadEmptyDomain(t *testing.T) {
	_, err := Load(context.Background(), "chrome", "")
	if err == nil {
		t.Error("expected error for empty domain")
	}
}
