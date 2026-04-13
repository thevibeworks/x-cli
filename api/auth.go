package api

import (
	"context"
	"encoding/json"
	"net/http"
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

// VerifyCredentials hits /1.1/account/verify_credentials.json and returns the
// user if the session is alive, or an AuthError otherwise.
func (c *Client) VerifyCredentials(ctx context.Context) (*User, error) {
	url := c.endpoints.Bases.REST + "/1.1/account/verify_credentials.json"
	resp, err := c.request(ctx, "GET", url, nil, requestOpts{authenticated: true, endpointName: "verifyCredentials"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &AuthError{Msg: "session invalid or expired", Status: resp.StatusCode}
	}
	if resp.StatusCode >= 400 {
		return nil, &APIError{Endpoint: "verifyCredentials", Status: resp.StatusCode}
	}

	var u User
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, err
	}
	if u.ID == "" {
		return nil, &AuthError{Msg: "verify_credentials returned empty user"}
	}
	c.sessionMu.Lock()
	c.session.User = &u
	c.sessionMu.Unlock()
	return &u, nil
}
