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
	Cookies      map[string]string
	Source       string // file path of the chosen cookie store
	Browser      string // matched browser name (chrome, firefox, ...)
	Profile      string // matched profile name (Default, Profile 6, ...)
	Alternatives []Match
}

// Match identifies one cookie store that contains the requested domain.
// Returned in Result.Alternatives so the caller can warn the user when
// auto-detect picked one of several candidates.
type Match struct {
	Browser string
	Profile string
	Source  string
	Count   int // number of cookies matched at this store
}

// Load reads cookies for the given domain from one or more local cookie
// stores belonging to the named browser. Returns the merged cookie map
// for the FIRST matching store, plus a list of the other stores it saw
// (Result.Alternatives) so the caller can warn the user about ambiguity.
//
// browser is matched case-insensitively against the known list. Pass ""
// to scan ALL detected browsers.
//
// profile is matched case-insensitively as a substring against the
// browser profile name (e.g. "default", "profile 6", "work"). Pass ""
// to accept any profile.
//
// domain is matched as a host suffix (so "x.com" picks up cookies on
// ".x.com" too).
func Load(ctx context.Context, browser, profile, domain string) (*Result, error) {
	if domain == "" {
		return nil, errors.New("browsercookies: domain required")
	}

	// Group cookies by (browser, profile, source) so we can build a
	// stable list of all matching stores in the order kooky yields them.
	type storeKey struct {
		browser string
		profile string
		source  string
	}
	type bucket struct {
		key     storeKey
		cookies map[string]string
	}
	var order []storeKey
	stores := map[storeKey]*bucket{}

	for c, err := range kooky.TraverseCookies(ctx, kooky.Valid, kooky.DomainHasSuffix(domain)) {
		if err != nil || c == nil || c.Browser == nil {
			continue
		}
		actualBrowser := c.Browser.Browser()
		actualProfile := c.Browser.Profile()
		actualPath := c.Browser.FilePath()
		if browser != "" && !strings.EqualFold(actualBrowser, browser) {
			continue
		}
		if profile != "" && !profileMatches(profile, actualProfile, actualPath) {
			continue
		}
		key := storeKey{browser: actualBrowser, profile: actualProfile, source: actualPath}
		b, ok := stores[key]
		if !ok {
			b = &bucket{key: key, cookies: map[string]string{}}
			stores[key] = b
			order = append(order, key)
		}
		// First-write-wins per store so later cookies in the same store
		// don't overwrite earlier ones.
		if _, exists := b.cookies[c.Name]; !exists {
			b.cookies[c.Name] = c.Value
		}
	}

	if len(order) == 0 {
		switch {
		case browser != "" && profile != "":
			return nil, fmt.Errorf("no cookies for %s in %s/%s — check `x auth browsers`", domain, browser, profile)
		case browser != "":
			return nil, fmt.Errorf("no cookies for %s in any %s profile — check `x auth browsers`", domain, browser)
		case profile != "":
			return nil, fmt.Errorf("no cookies for %s in any profile matching %q — check `x auth browsers`", domain, profile)
		default:
			return nil, fmt.Errorf("no cookies for %s in any local browser cookie store", domain)
		}
	}

	// Choose the first match. The rest are "alternatives" the caller
	// can surface to the user.
	chosen := stores[order[0]]
	alts := make([]Match, 0, len(order)-1)
	for _, k := range order[1:] {
		b := stores[k]
		alts = append(alts, Match{
			Browser: k.browser,
			Profile: k.profile,
			Source:  k.source,
			Count:   len(b.cookies),
		})
	}

	return &Result{
		Cookies:      chosen.cookies,
		Source:       chosen.key.source,
		Browser:      chosen.key.browser,
		Profile:      chosen.key.profile,
		Alternatives: alts,
	}, nil
}

// List enumerates every cookie store that has at least one cookie for
// the given domain. Used by `x auth browsers` to show the user which
// (browser, profile) pairs are available before they pin one.
func List(ctx context.Context, domain string) ([]Match, error) {
	if domain == "" {
		return nil, errors.New("browsercookies: domain required")
	}
	type key struct {
		browser, profile, source string
	}
	seen := map[key]int{}
	var order []key
	for c, err := range kooky.TraverseCookies(ctx, kooky.Valid, kooky.DomainHasSuffix(domain)) {
		if err != nil || c == nil || c.Browser == nil {
			continue
		}
		k := key{
			browser: c.Browser.Browser(),
			profile: c.Browser.Profile(),
			source:  c.Browser.FilePath(),
		}
		if _, ok := seen[k]; !ok {
			order = append(order, k)
		}
		seen[k]++
	}
	out := make([]Match, 0, len(order))
	for _, k := range order {
		out = append(out, Match{
			Browser: k.browser,
			Profile: k.profile,
			Source:  k.source,
			Count:   seen[k],
		})
	}
	return out, nil
}

// profileMatches returns true when `want` (the user-supplied --profile
// substring) matches either the human profile name from kooky (e.g.
// "Tammie", "Default", "Work") OR a path component of the cookie file
// (e.g. "Profile 6" from ".../Chrome/Profile 6/Cookies"). Case-
// insensitive substring on both. This keeps `--profile "Profile 6"`
// working alongside `--profile tammie`.
func profileMatches(want, name, path string) bool {
	w := strings.ToLower(want)
	if strings.Contains(strings.ToLower(name), w) {
		return true
	}
	// Match against the directory path so users can pass the on-disk
	// "Profile N" name they see in `x auth browsers`.
	return strings.Contains(strings.ToLower(path), w)
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
