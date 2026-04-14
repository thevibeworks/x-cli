package api

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Session holds the cookies and derived tokens for one authenticated user.
// All fields are opaque to callers except through the methods on *Client.
type Session struct {
	Cookies map[string]string
	User    *User
}

type User struct {
	ID       string `json:"id_str"`
	Username string `json:"screen_name"`
	Name     string `json:"name"`
}

// ParseCookieString accepts a browser-exported header like
//
//	auth_token=abc; ct0=def; twid=u%3D123
//
// and returns a cookie map. Empty names and empty values are dropped so
// that pasting stale `document.cookie` output does not produce `name=`
// entries that X's gateway may reject. Values are NOT URL-decoded — `twid`
// and friends are echoed raw on the wire.
func ParseCookieString(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		idx := strings.IndexByte(pair, '=')
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(pair[:idx])
		val := strings.TrimSpace(pair[idx+1:])
		if name == "" || val == "" {
			continue
		}
		out[name] = val
	}
	return out
}

// RequireAuthCookies returns an AuthError if the required cookies are
// absent. `auth_token` is mandatory always. `ct0` is mandatory ONLY for
// the http+utls transport — when the browser transport is in use, the
// browser fetches a fresh ct0 from x.com on the first navigation, so
// the caller can pass auth_token alone and let chromebrowser do the
// rest.
//
// The caller signals which transport is in play via the boolean.
// `false` (browser path) accepts an empty ct0; `true` (http path)
// requires it.
func RequireAuthCookies(cookies map[string]string) error {
	return RequireAuthCookiesFor(cookies, true)
}

// RequireAuthCookiesFor is the explicit form. Pass needCt0 = false if
// the caller will go through the browser transport (which can mint
// ct0 on the fly via Set-Cookie from x.com).
func RequireAuthCookiesFor(cookies map[string]string, needCt0 bool) error {
	if cookies["auth_token"] == "" {
		return &AuthError{Msg: "missing auth_token cookie"}
	}
	if needCt0 && cookies["ct0"] == "" {
		return &AuthError{Msg: "missing ct0 (CSRF) cookie"}
	}
	return nil
}

// VerifyCredentials confirms the imported session is alive and returns
// the logged-in user.
//
// X removed the legacy /1.1/account/verify_credentials.json endpoint
// (returns 404 as of April 2026). x-cli reads the `twid` cookie that
// the user pasted, parses the numeric user id out of it, and calls
// UserByRestId to confirm the session is alive AND fetch identity in
// one round trip.
//
// When the user only provided `auth_token` (no twid — typical when
// pasting from DevTools or when going through chromebrowser without
// a stored twid), VerifyCredentials makes a cheap probe call. The
// browser transport navigates to x.com on the first request, x.com
// Set-Cookie's a fresh twid (and ct0, gt, etc.), the transport
// surfaces those cookies back via Set-Cookie response headers, and
// client.mergeSetCookies folds them into the session. We then
// re-read twid and retry UserByRestId.
func (c *Client) VerifyCredentials(ctx context.Context) (*User, error) {
	if user, err := c.verifyByTwid(ctx); err == nil {
		return user, nil
	} else if !isMissingTwid(err) {
		return nil, err
	}

	// No twid yet. Run any cheap GraphQL call to make the browser
	// transport navigate to x.com — that's what triggers the
	// Set-Cookie response that populates twid + ct0 in the session.
	if _, err := c.GetProfile(ctx, "twitter"); err != nil {
		return nil, normaliseAuthError(err)
	}

	// After the probe, the session should now have twid (merged in
	// via mergeSetCookies). Retry the proper verify.
	user, err := c.verifyByTwid(ctx)
	if err != nil {
		return nil, fmt.Errorf("after probe: %w", err)
	}
	return user, nil
}

// verifyByTwid is the "have-twid" branch of VerifyCredentials. Returns
// errMissingTwid when no usable twid is present, or any other error
// from the underlying UserByRestId call.
func (c *Client) verifyByTwid(ctx context.Context) (*User, error) {
	c.sessionMu.RLock()
	twidRaw := ""
	if c.session.Cookies != nil {
		twidRaw = c.session.Cookies["twid"]
	}
	c.sessionMu.RUnlock()

	userID := parseTwidUserID(twidRaw)
	if userID == "" {
		return nil, errMissingTwid
	}
	p, err := c.GetProfileByID(ctx, userID)
	if err != nil {
		return nil, normaliseAuthError(err)
	}
	u := &User{
		ID:       p.RestID,
		Username: p.ScreenName,
		Name:     p.Name,
	}
	c.sessionMu.Lock()
	c.session.User = u
	c.sessionMu.Unlock()
	return u, nil
}

// errMissingTwid is the sentinel returned by verifyByTwid when no
// usable twid cookie is present in the session. VerifyCredentials
// uses isMissingTwid to detect it and trigger the probe-then-retry
// path.
var errMissingTwid = &AuthError{Msg: "twid cookie not yet present — probe needed"}

func isMissingTwid(err error) bool { return err == errMissingTwid }

// parseTwidUserID extracts the numeric user ID from a `twid` cookie value.
// Twitter encodes it as `u%3D<NNN>` (URL-encoded `u=NNN`).
//
// Examples:
//
//	"u%3D2017830703355072513" → "2017830703355072513"
//	"u=2017830703355072513"   → "2017830703355072513"
//	""                        → ""
func parseTwidUserID(raw string) string {
	if raw == "" {
		return ""
	}
	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		decoded = raw
	}
	if !strings.HasPrefix(decoded, "u=") {
		return ""
	}
	id := decoded[2:]
	for _, r := range id {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return id
}

// normaliseAuthError maps domain errors to AuthError when the underlying
// failure looks like a session problem. NotFoundError on a self-lookup
// almost always means the session id is no longer valid.
func normaliseAuthError(err error) error {
	if err == nil {
		return nil
	}
	switch e := err.(type) {
	case *AuthError:
		return e
	case *NotFoundError:
		return &AuthError{Msg: "session invalid or expired (self-lookup not found)"}
	}
	return err
}
