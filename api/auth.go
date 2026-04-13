package api

import (
	"context"
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

// RequireAuthCookies returns an AuthError if the required cookies are absent.
func RequireAuthCookies(cookies map[string]string) error {
	if cookies["auth_token"] == "" {
		return &AuthError{Msg: "missing auth_token cookie"}
	}
	if cookies["ct0"] == "" {
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
// one round trip. Falls back to a UserByScreenName "twitter" probe if
// `twid` is missing — that's the cheapest "is this cookie usable at
// all" sanity check we have without it.
func (c *Client) VerifyCredentials(ctx context.Context) (*User, error) {
	c.sessionMu.RLock()
	cookies := c.session.Cookies
	twidRaw := ""
	if cookies != nil {
		twidRaw = cookies["twid"]
	}
	c.sessionMu.RUnlock()

	if userID := parseTwidUserID(twidRaw); userID != "" {
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

	// No twid: probe with a known account so we still detect a dead
	// cookie, but we cannot return identity in this branch.
	if _, err := c.GetProfile(ctx, "twitter"); err != nil {
		return nil, normaliseAuthError(err)
	}
	return nil, &AuthError{Msg: "session is alive but `twid` cookie missing — re-import a complete cookie string"}
}

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
