package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client is the HTTP transport for x-cli. One per authenticated session.
//
// It owns the endpoint map, the throttle, the underlying http.Client (which
// may be wrapped with a uTLS round-tripper in a future release), and the
// currently loaded session cookies. All GraphQL and REST helpers on domain
// files (profile.go, tweets.go, etc.) go through Client.GraphQL / Client.REST.
//
// Client protects `session` with an RWMutex so that mid-request cookie
// rotation (mergeSetCookies) cannot race with header construction in
// applyHeaders. Callers can still share one Client across goroutines.
type Client struct {
	endpoints  *EndpointMap
	throttle   *Throttle
	httpClient *http.Client

	sessionMu sync.RWMutex
	session   Session

	userIDCache sync.Map // screen_name(lowercased) → rest_id

	userAgent    string
	retryBackoff time.Duration // base unit for exponential backoff; overridable in tests
}

type Options struct {
	Endpoints  *EndpointMap
	Throttle   *Throttle
	HTTPClient *http.Client
	Session    Session
	UserAgent  string
}

func New(opts Options) *Client {
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.UserAgent == "" {
		opts.UserAgent = defaultUserAgent
	}
	if opts.Throttle == nil {
		opts.Throttle = NewThrottle(Defaults{})
	}
	return &Client{
		endpoints:    opts.Endpoints,
		throttle:     opts.Throttle,
		httpClient:   opts.HTTPClient,
		session:      opts.Session,
		userAgent:    opts.UserAgent,
		retryBackoff: time.Second,
	}
}

// setRetryBackoff lets tests shorten exponential backoff. Not exported.
func (c *Client) setRetryBackoff(d time.Duration) { c.retryBackoff = d }

// Matched to Chrome 120 on macOS. Keep this synced with TLS/client-hint headers.
const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// Session returns a shallow copy of the current session for inspection. The
// returned cookie map is a fresh allocation, safe to read without holding the
// client lock.
func (c *Client) Session() Session {
	c.sessionMu.RLock()
	defer c.sessionMu.RUnlock()
	cp := Session{User: c.session.User}
	if c.session.Cookies != nil {
		cp.Cookies = make(map[string]string, len(c.session.Cookies))
		for k, v := range c.session.Cookies {
			cp.Cookies[k] = v
		}
	}
	return cp
}

func (c *Client) Endpoints() *EndpointMap { return c.endpoints }

// -----------------------------------------------------------------------------
// Core request
// -----------------------------------------------------------------------------

type requestOpts struct {
	authenticated bool
	endpointName  string
	extraHeaders  map[string]string
	maxRetries    int
}

func (c *Client) request(ctx context.Context, method, rawURL string, body io.Reader, opts requestOpts) (*http.Response, error) {
	maxRetries := opts.maxRetries
	if maxRetries == 0 {
		maxRetries = 3
	}

	var buf []byte
	if body != nil {
		b, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		buf = b
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		var reqBody io.Reader
		if buf != nil {
			reqBody = bytes.NewReader(buf)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, reqBody)
		if err != nil {
			return nil, err
		}
		c.applyHeaders(req, opts)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = &NetworkError{Endpoint: opts.endpointName, Err: err}
			if attempt < maxRetries {
				if err := sleep(ctx, c.backoffFor(attempt)); err != nil {
					return nil, err
				}
				continue
			}
			return nil, lastErr
		}

		c.throttle.Observe(resp.StatusCode, parseRateReset(resp))
		c.mergeSetCookies(resp)

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			if attempt < maxRetries {
				wait := waitFromRateReset(resp)
				if err := sleep(ctx, wait); err != nil {
					return nil, err
				}
				continue
			}
			return nil, &RateLimitError{Endpoint: opts.endpointName, ResetAt: parseRateReset(resp)}
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return resp, nil
		}
		if resp.StatusCode == http.StatusNotFound {
			return resp, nil
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			if attempt < maxRetries {
				if err := sleep(ctx, c.backoffFor(attempt)); err != nil {
					return nil, err
				}
				continue
			}
			return nil, &APIError{Endpoint: opts.endpointName, Status: resp.StatusCode}
		}

		return resp, nil
	}

	if lastErr == nil {
		lastErr = errors.New("request failed after retries")
	}
	return nil, lastErr
}

func (c *Client) applyHeaders(req *http.Request, opts requestOpts) {
	bearer := c.endpoints.Bearer
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("x-twitter-active-user", "yes")
	req.Header.Set("x-twitter-client-language", "en")

	// Client-hint headers matched to the UA. Pinned, not rotated per-request.
	req.Header.Set("sec-ch-ua", `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"macOS"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")

	if opts.authenticated {
		c.sessionMu.RLock()
		cookies := c.session.Cookies
		if cookies != nil {
			if ct0 := cookies["ct0"]; ct0 != "" {
				req.Header.Set("x-csrf-token", ct0)
			}
			req.Header.Set("x-twitter-auth-type", "OAuth2Session")
			req.Header.Set("Cookie", buildCookieHeader(cookies))
		}
		c.sessionMu.RUnlock()
	}

	if req.Body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	for k, v := range opts.extraHeaders {
		req.Header.Set(k, v)
	}
}

func buildCookieHeader(cookies map[string]string) string {
	parts := make([]string, 0, len(cookies))
	for k, v := range cookies {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

// rotatableCookies is the set of cookie names x-cli will accept from a
// mid-session Set-Cookie header. Anything outside this list (marketing
// beacons, ad trackers, etc.) gets ignored so that a rogue or stale
// response cannot poison the session.
var rotatableCookies = map[string]struct{}{
	"ct0":        {},
	"auth_token": {},
	"att":        {},
	"kdt":        {},
	"twid":       {},
	"guest_id":   {},
}

// mergeSetCookies updates the in-memory session with any Set-Cookie headers
// returned by the server. X rotates `ct0` periodically mid-session, and a
// long-running scrape that ignores the rotation will start failing.
//
// Safety rules:
//   - Only names in `rotatableCookies` are accepted.
//   - Empty values and deletion directives (`MaxAge <= 0`, `Expires` in the
//     past) are ignored so a single bad response cannot nuke a live session.
//   - Persisting the rotated value back to disk is the caller's
//     responsibility (future work — see docs/comparison-xactions.md §2.11).
func (c *Client) mergeSetCookies(resp *http.Response) {
	fresh := resp.Cookies()
	if len(fresh) == 0 {
		return
	}

	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if c.session.Cookies == nil {
		c.session.Cookies = make(map[string]string, len(fresh))
	}
	for _, ck := range fresh {
		if ck.Name == "" || ck.Value == "" {
			continue
		}
		if _, ok := rotatableCookies[ck.Name]; !ok {
			continue
		}
		if ck.MaxAge < 0 {
			continue
		}
		if !ck.Expires.IsZero() && ck.Expires.Before(time.Now()) {
			continue
		}
		c.session.Cookies[ck.Name] = ck.Value
	}
}

// backoffFor computes exponential backoff with jitter for retry attempt n.
// The base is c.retryBackoff, which tests can shorten via setRetryBackoff.
// attempt is clamped to 16 (~65k× the base) so the multiplier does not
// overflow time.Duration for pathological maxRetries values.
func (c *Client) backoffFor(attempt int) time.Duration {
	base := c.retryBackoff
	if base <= 0 {
		base = time.Second
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 16 {
		attempt = 16
	}
	mul := time.Duration(1 << attempt)
	var jitter time.Duration
	if half := int64(base / 2); half > 0 {
		jitter = time.Duration(rand.Int63n(half))
	}
	return base*mul + jitter
}

// -----------------------------------------------------------------------------
// GraphQL + REST helpers
// -----------------------------------------------------------------------------

// GraphQL executes a named GraphQL query from the endpoint map.
//
// result must be a pointer the caller can JSON-unmarshal into. The raw
// response body is consumed and closed by this function.
func (c *Client) GraphQL(ctx context.Context, name string, variables map[string]any, result any) error {
	ep, ok := c.endpoints.GraphQL[name]
	if !ok {
		return fmt.Errorf("unknown graphql endpoint %q", name)
	}

	if err := c.throttle.AwaitRead(ctx, name, ep.RPS, ep.Burst); err != nil {
		return err
	}

	if variables == nil {
		variables = map[string]any{}
	}
	varsJSON, err := json.Marshal(variables)
	if err != nil {
		return err
	}
	featsJSON, err := json.Marshal(c.endpoints.Features)
	if err != nil {
		return err
	}

	q := url.Values{}
	q.Set("variables", string(varsJSON))
	q.Set("features", string(featsJSON))
	rawURL := fmt.Sprintf("%s/%s/%s?%s", c.endpoints.Bases.GraphQL, ep.QueryID, ep.OperationName, q.Encode())

	resp, err := c.request(ctx, "GET", rawURL, nil, requestOpts{authenticated: true, endpointName: name})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &AuthError{Msg: "graphql " + name + " rejected session", Status: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusNotFound {
		return &NotFoundError{Endpoint: name}
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return &APIError{Endpoint: name, Status: resp.StatusCode, Body: string(body)}
	}

	if result == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(result)
}

// REST executes a REST endpoint (v1.1 family), typically for mutations.
func (c *Client) REST(ctx context.Context, name string, form url.Values, result any) error {
	ep, ok := c.endpoints.REST[name]
	if !ok {
		return fmt.Errorf("unknown rest endpoint %q", name)
	}

	if ep.Kind == "mutation" {
		if err := c.throttle.AwaitMutation(ctx, ep.MinGap, ep.MaxGap, ep.DailyCap); err != nil {
			return err
		}
	} else {
		if err := c.throttle.AwaitRead(ctx, name, 0, 0); err != nil {
			return err
		}
	}

	rawURL := c.endpoints.Bases.REST + ep.Path
	var body io.Reader
	headers := map[string]string{}
	if ep.Method == "POST" {
		headers["Content-Type"] = "application/x-www-form-urlencoded"
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
	} else if form != nil {
		rawURL = rawURL + "?" + form.Encode()
	}

	resp, err := c.request(ctx, ep.Method, rawURL, body, requestOpts{
		authenticated: true,
		endpointName:  name,
		extraHeaders:  headers,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &AuthError{Msg: "rest " + name + " rejected session", Status: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusNotFound {
		return &NotFoundError{Endpoint: name}
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return &APIError{Endpoint: name, Status: resp.StatusCode, Body: string(b)}
	}

	if result == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(result)
}

// -----------------------------------------------------------------------------
// Cursor extraction (walks timeline instruction shapes)
// -----------------------------------------------------------------------------

// ExtractBottomCursor walks a decoded GraphQL response for a timeline entry
// whose `entryId` starts with `cursor-bottom`. Used by paginated endpoints
// (Followers, UserTweets, SearchTimeline, etc.).
//
// Implementation detail: the walk is a breadth-first search with
// key-sorted traversal so that responses containing multiple nested
// timelines always return the same cursor (the outermost, leftmost by
// key sort order). Go's map iteration is randomized, so a naive DFS
// would return a different cursor on different runs.
func ExtractBottomCursor(v any) string {
	return walkForCursor(v, "cursor-bottom")
}

func walkForCursor(root any, prefix string) string {
	queue := []any{root}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]

		switch x := node.(type) {
		case map[string]any:
			if cur := extractCursorFromInstructions(x, prefix); cur != "" {
				return cur
			}
			// Enqueue children in a deterministic order.
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				queue = append(queue, x[k])
			}
		case []any:
			for _, child := range x {
				queue = append(queue, child)
			}
		}
	}
	return ""
}

// extractCursorFromInstructions looks for an `instructions` array in the
// current node and walks its entries for one matching `prefix`. Returns
// "" when no cursor is found at this level — the caller recurses.
func extractCursorFromInstructions(m map[string]any, prefix string) string {
	insts, ok := m["instructions"].([]any)
	if !ok {
		return ""
	}
	for _, inst := range insts {
		im, ok := inst.(map[string]any)
		if !ok {
			continue
		}
		entries, ok := im["entries"].([]any)
		if !ok {
			continue
		}
		for _, e := range entries {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			id, _ := em["entryId"].(string)
			if !strings.HasPrefix(id, prefix) {
				continue
			}
			content, ok := em["content"].(map[string]any)
			if !ok {
				continue
			}
			if val, _ := content["value"].(string); val != "" {
				return val
			}
			if ic, ok := content["itemContent"].(map[string]any); ok {
				if val, _ := ic["value"].(string); val != "" {
					return val
				}
			}
		}
	}
	return ""
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func parseRateReset(resp *http.Response) int64 {
	v := resp.Header.Get("x-rate-limit-reset")
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func waitFromRateReset(resp *http.Response) time.Duration {
	reset := parseRateReset(resp)
	if reset == 0 {
		return 30 * time.Second
	}
	d := time.Until(time.Unix(reset, 0))
	if d < time.Second {
		return time.Second
	}
	if d > 10*time.Minute {
		return 10 * time.Minute
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
