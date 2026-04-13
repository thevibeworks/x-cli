// Package browsercookies reads a browser's local cookie store directly
// from disk and decrypts the values, so x-cli can import an X session
// without a manual paste from DevTools.
//
// Mechanics by browser:
//
//	Chrome / Brave / Edge / Chromium  →  SQLite at <profile>/Cookies,
//	                                      AES-128-CBC encrypted values
//	                                      with the per-OS Safe Storage key
//	                                      (macOS Keychain entry, Linux
//	                                      libsecret/kwallet, Windows DPAPI)
//	Firefox                            →  SQLite at <profile>/cookies.sqlite,
//	                                      values stored plaintext
//
// Caveats:
//
//   - Chrome on macOS locks the cookie file while running. The user must
//     close Chrome before importing.
//   - macOS prompts for Keychain access on first read of the Safe Storage
//     key (one-time per binary). The system dialog says
//     "x wants to access key 'Chrome' in your keychain" — that is normal.
//   - Linux Chrome on a headless box without libsecret/kwallet falls back
//     to a hardcoded "peanuts" salt; kooky handles both paths.
//   - This is a READ-ONLY operation. We never modify the browser's store.
package browsercookies

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/browserutils/kooky"

	// Driver imports — each adds support for one browser family.
	_ "github.com/browserutils/kooky/browser/brave"
	_ "github.com/browserutils/kooky/browser/chrome"
	_ "github.com/browserutils/kooky/browser/chromium"
	_ "github.com/browserutils/kooky/browser/edge"
	_ "github.com/browserutils/kooky/browser/firefox"
)

// Browsers is the canonical list of browser names accepted by Load.
var Browsers = []string{"chrome", "firefox", "brave", "edge", "chromium"}

// Result is what Load returns: a flat map of cookie name → value, plus
// some diagnostic context for the caller to render or log.
type Result struct {
	Cookies map[string]string
	Source  string // human-readable description of the cookie store path
	Browser string // matched browser name
}

// Load reads cookies for the given domain from one or more local cookie
// stores belonging to the named browser. Returns the merged cookie map.
//
// browser is matched case-insensitively against the known list. Pass ""
// to scan ALL detected browsers — useful when you don't care which
// browser is logged in, just that some browser is.
//
// domain is matched as a host suffix (so "x.com" picks up cookies on
// ".x.com" too).
func Load(ctx context.Context, browser, domain string) (*Result, error) {
	if domain == "" {
		return nil, errors.New("browsercookies: domain required")
	}

	out := map[string]string{}
	matchedBrowser := ""
	matchedSource := ""

	// kooky.TraverseCookies returns an iter.Seq2[*Cookie, error] that
	// walks every registered cookie store finder and yields cookies as
	// it goes. We filter by browser in our own code so the caller can
	// pass an empty string to mean "any browser".
	for c, err := range kooky.TraverseCookies(ctx, kooky.Valid, kooky.DomainHasSuffix(domain)) {
		if err != nil {
			// Skip unreadable stores instead of bailing — kooky reports
			// per-store failures as soft errors. We surface only "found
			// nothing" at the end.
			continue
		}
		if c == nil || c.Browser == nil {
			continue
		}
		actual := c.Browser.Browser()
		if browser != "" && !strings.EqualFold(actual, browser) {
			continue
		}
		// First write wins so the most recently traversed store's
		// values are preserved. kooky lists default profiles first.
		if _, exists := out[c.Name]; !exists {
			out[c.Name] = c.Value
		}
		if matchedBrowser == "" {
			matchedBrowser = actual
			matchedSource = c.Browser.FilePath()
		}
	}

	if len(out) == 0 {
		if browser != "" {
			return nil, fmt.Errorf("no cookies for %s in any %s cookie store — make sure %s is installed and you're logged in", domain, browser, browser)
		}
		return nil, fmt.Errorf("no cookies for %s in any local browser cookie store", domain)
	}

	return &Result{
		Cookies: out,
		Source:  matchedSource,
		Browser: matchedBrowser,
	}, nil
}

// FormatCookieHeader joins the relevant subset of a cookie map into a
// `name=value; name=value` string suitable for `Cookie:` headers and
// for x-cli's auth-import parser. Only names in `wanted` are kept; pass
// nil to keep everything.
func FormatCookieHeader(cookies map[string]string, wanted []string) string {
	if cookies == nil {
		return ""
	}
	if wanted == nil {
		parts := make([]string, 0, len(cookies))
		for k, v := range cookies {
			if v == "" {
				continue
			}
			parts = append(parts, k+"="+v)
		}
		return strings.Join(parts, "; ")
	}
	parts := make([]string, 0, len(wanted))
	for _, k := range wanted {
		if v, ok := cookies[k]; ok && v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, "; ")
}
