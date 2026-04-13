package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient wires a Client at the given httptest server with a throttle
// generous enough that it never blocks a test.
func newTestClient(t *testing.T, serverURL string, graphql map[string]GraphQLEndpoint, cookies map[string]string) *Client {
	t.Helper()
	eps := &EndpointMap{
		Bases:    Bases{GraphQL: serverURL, REST: serverURL, API: serverURL},
		Bearer:   "TESTBEARER",
		Features: map[string]bool{"test_feature": true},
		GraphQL:  graphql,
	}
	if eps.GraphQL == nil {
		eps.GraphQL = map[string]GraphQLEndpoint{}
	}
	c := New(Options{
		Endpoints: eps,
		Throttle:  NewThrottle(Defaults{}),
		Session:   Session{Cookies: cookies},
	})
	c.setRetryBackoff(5 * time.Millisecond)
	return c
}

func TestApplyHeadersUnauthenticated(t *testing.T) {
	var captured http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil, nil)

	resp, err := c.request(context.Background(), "GET", srv.URL+"/x",
		nil, requestOpts{authenticated: false, endpointName: "t"})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := captured.Get("Authorization"); got != "Bearer TESTBEARER" {
		t.Errorf("Authorization = %q", got)
	}
	if got := captured.Get("User-Agent"); !strings.Contains(got, "Chrome") {
		t.Errorf("User-Agent = %q", got)
	}
	if got := captured.Get("sec-ch-ua"); got == "" {
		t.Errorf("missing sec-ch-ua client hint")
	}
	if got := captured.Get("sec-fetch-dest"); got != "empty" {
		t.Errorf("sec-fetch-dest = %q", got)
	}
	if got := captured.Get("x-csrf-token"); got != "" {
		t.Errorf("unauthenticated request should not carry x-csrf-token, got %q", got)
	}
	if got := captured.Get("Cookie"); got != "" {
		t.Errorf("unauthenticated request should not carry Cookie, got %q", got)
	}
	if got := captured.Get("x-twitter-auth-type"); got != "" {
		t.Errorf("unauthenticated request should not carry x-twitter-auth-type, got %q", got)
	}
}

func TestApplyHeadersAuthenticated(t *testing.T) {
	var captured http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil, map[string]string{
		"auth_token": "AUTHTOK",
		"ct0":        "CSRFTOK",
		"twid":       "u%3D99",
	})

	resp, err := c.request(context.Background(), "GET", srv.URL+"/y",
		nil, requestOpts{authenticated: true, endpointName: "t"})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := captured.Get("x-csrf-token"); got != "CSRFTOK" {
		t.Errorf("x-csrf-token = %q", got)
	}
	if got := captured.Get("x-twitter-auth-type"); got != "OAuth2Session" {
		t.Errorf("x-twitter-auth-type = %q", got)
	}
	ck := captured.Get("Cookie")
	for _, want := range []string{"auth_token=AUTHTOK", "ct0=CSRFTOK", "twid=u%3D99"} {
		if !strings.Contains(ck, want) {
			t.Errorf("Cookie header %q missing %q", ck, want)
		}
	}
}

func TestSetCookieMergeOnResponse(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.SetCookie(w, &http.Cookie{Name: "ct0", Value: "ROTATED"})
			http.SetCookie(w, &http.Cookie{Name: "att", Value: "NEW"})
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil, map[string]string{
		"auth_token": "AUTHTOK",
		"ct0":        "ORIGINAL",
	})

	resp, err := c.request(context.Background(), "GET", srv.URL+"/rotate",
		nil, requestOpts{authenticated: true, endpointName: "t"})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	sess := c.Session()
	if got := sess.Cookies["ct0"]; got != "ROTATED" {
		t.Errorf("ct0 not rotated, got %q", got)
	}
	if got := sess.Cookies["att"]; got != "NEW" {
		t.Errorf("new att cookie not merged, got %q", got)
	}
	if got := sess.Cookies["auth_token"]; got != "AUTHTOK" {
		t.Errorf("unrelated cookie clobbered, got %q", got)
	}
}

func TestMergeSetCookiesRejectsDeletionDirective(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "auth_token", Value: "ATTACKER", MaxAge: -1})
		http.SetCookie(w, &http.Cookie{Name: "ct0", Value: "ATTACKER", MaxAge: 0, Expires: time.Unix(1, 0)})
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil, map[string]string{
		"auth_token": "ORIGINAL",
		"ct0":        "ORIGINAL",
	})
	resp, err := c.request(context.Background(), "GET", srv.URL+"/attack",
		nil, requestOpts{authenticated: true, endpointName: "t"})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	sess := c.Session()
	if sess.Cookies["auth_token"] != "ORIGINAL" {
		t.Errorf("deletion directive overwrote auth_token: %q", sess.Cookies["auth_token"])
	}
	if sess.Cookies["ct0"] != "ORIGINAL" {
		t.Errorf("deletion directive overwrote ct0: %q", sess.Cookies["ct0"])
	}
}

func TestMergeSetCookiesIgnoresUnrelatedNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "marketing_id", Value: "ABC"})
		http.SetCookie(w, &http.Cookie{Name: "ct0", Value: "GOOD"})
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil, map[string]string{"auth_token": "x", "ct0": "OLD"})
	resp, _ := c.request(context.Background(), "GET", srv.URL+"/x",
		nil, requestOpts{authenticated: true, endpointName: "t"})
	resp.Body.Close()

	sess := c.Session()
	if _, ok := sess.Cookies["marketing_id"]; ok {
		t.Error("marketing_id should not be merged")
	}
	if sess.Cookies["ct0"] != "GOOD" {
		t.Errorf("ct0 = %q", sess.Cookies["ct0"])
	}
}

func TestGraphQLSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/qid1/UserByScreenName") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		vars := r.URL.Query().Get("variables")
		if !strings.Contains(vars, "jack") {
			t.Errorf("variables missing jack: %q", vars)
		}
		feats := r.URL.Query().Get("features")
		if !strings.Contains(feats, "test_feature") {
			t.Errorf("features missing test_feature: %q", feats)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data":{"user":{"result":{"rest_id":"42","is_blue_verified":true,"legacy":{"screen_name":"jack","name":"Jack","followers_count":1000,"friends_count":500,"statuses_count":42}}}}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, map[string]GraphQLEndpoint{
		"UserByScreenName": {
			QueryID: "qid1", OperationName: "UserByScreenName",
			Kind: "read", RPS: 100, Burst: 10,
		},
	}, map[string]string{"auth_token": "x", "ct0": "y"})

	var raw map[string]any
	if err := c.GraphQL(context.Background(), "UserByScreenName",
		map[string]any{"screen_name": "jack"}, &raw); err != nil {
		t.Fatalf("GraphQL: %v", err)
	}

	p := extractProfile(raw)
	if p == nil {
		t.Fatal("extractProfile returned nil")
	}
	if p.ScreenName != "jack" || p.Followers != 1000 || !p.Verified {
		t.Errorf("profile = %+v", p)
	}
}

func TestGraphQLUnknownEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, nil, map[string]string{"auth_token": "x", "ct0": "y"})
	err := c.GraphQL(context.Background(), "Nonexistent", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown graphql endpoint") {
		t.Errorf("want unknown-endpoint error, got %v", err)
	}
}

func TestGraphQL404ReturnsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, map[string]GraphQLEndpoint{
		"Op": {QueryID: "q", OperationName: "Op", Kind: "read", RPS: 100, Burst: 10},
	}, map[string]string{"auth_token": "x", "ct0": "y"})
	err := c.GraphQL(context.Background(), "Op", nil, nil)
	if _, ok := err.(*NotFoundError); !ok {
		t.Errorf("want *NotFoundError, got %T: %v", err, err)
	}
}

func TestGraphQL401ReturnsAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, map[string]GraphQLEndpoint{
		"Op": {QueryID: "q", OperationName: "Op", Kind: "read", RPS: 100, Burst: 10},
	}, map[string]string{"auth_token": "bad", "ct0": "bad"})
	err := c.GraphQL(context.Background(), "Op", nil, nil)
	if _, ok := err.(*AuthError); !ok {
		t.Errorf("want *AuthError, got %T: %v", err, err)
	}
}

func TestRetryOn5xxThenSuccess(t *testing.T) {
	// Default retry budget is 1 → 2 attempts total. First call returns
	// 500, retry returns 200. Validates the retry path without relying
	// on a specific maxRetries value.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 2 {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, map[string]GraphQLEndpoint{
		"Op": {QueryID: "q", OperationName: "Op", Kind: "read", RPS: 100, Burst: 10},
	}, map[string]string{"auth_token": "x", "ct0": "y"})

	var raw map[string]any
	if err := c.GraphQL(context.Background(), "Op", nil, &raw); err != nil {
		t.Fatalf("GraphQL: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("expected 2 calls, got %d", atomic.LoadInt32(&calls))
	}
	if v, _ := raw["ok"].(bool); !v {
		t.Error("ok field not decoded")
	}
}

func TestRateLimit429WithReset(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("x-rate-limit-reset", "1") // ancient → waitFromRateReset clamps to 1s min
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, map[string]GraphQLEndpoint{
		"Op": {QueryID: "q", OperationName: "Op", Kind: "read", RPS: 100, Burst: 10},
	}, map[string]string{"auth_token": "x", "ct0": "y"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var raw map[string]any
	if err := c.GraphQL(ctx, "Op", nil, &raw); err != nil {
		t.Fatalf("GraphQL: %v", err)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Errorf("expected retry after 429, got %d calls", atomic.LoadInt32(&calls))
	}
}

func TestVerifyCredentialsSuccess(t *testing.T) {
	// Modern path: VerifyCredentials reads the `twid` cookie, parses
	// the user ID, and calls UserByRestId. The fake server responds
	// with a modern-shape user (core.screen_name + core.name).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/uid_qid/UserByRestId") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"user":{"result":{"__typename":"User","rest_id":"123","is_blue_verified":true,"core":{"screen_name":"jack","name":"Jack Dorsey","created_at":"Tue Mar 21 20:50:14 +0000 2006"},"legacy":{"description":"hi","followers_count":7000000}}}}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, map[string]GraphQLEndpoint{
		"UserByRestId": {QueryID: "uid_qid", OperationName: "UserByRestId", Kind: "read", RPS: 100, Burst: 10},
	}, map[string]string{"auth_token": "x", "ct0": "y", "twid": "u%3D123"})

	u, err := c.VerifyCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != "123" || u.Username != "jack" || u.Name != "Jack Dorsey" {
		t.Errorf("user = %+v", u)
	}
}

func TestVerifyCredentialsUnauthorized(t *testing.T) {
	// Server returns 401 on UserByRestId; VerifyCredentials must surface
	// it as *AuthError, not a raw GraphQL error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, map[string]GraphQLEndpoint{
		"UserByRestId": {QueryID: "uid_qid", OperationName: "UserByRestId", Kind: "read", RPS: 100, Burst: 10},
	}, map[string]string{"auth_token": "bad", "ct0": "bad", "twid": "u%3D123"})

	_, err := c.VerifyCredentials(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*AuthError); !ok {
		t.Fatalf("want *AuthError, got %T: %v", err, err)
	}
}

func TestParseTwidUserID(t *testing.T) {
	cases := map[string]string{
		"u%3D2017830703355072513": "2017830703355072513",
		"u=2017830703355072513":   "2017830703355072513",
		"u%3D12":                  "12",
		"":                         "",
		"garbage":                  "",
		"u%3Dnot-a-number":        "",
		"u%3D":                     "",
	}
	for in, want := range cases {
		if got := parseTwidUserID(in); got != want {
			t.Errorf("parseTwidUserID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractBottomCursorDirect(t *testing.T) {
	raw := map[string]any{
		"data": map[string]any{
			"timeline": map[string]any{
				"instructions": []any{
					map[string]any{
						"entries": []any{
							map[string]any{
								"entryId": "tweet-12345",
								"content": map[string]any{},
							},
							map[string]any{
								"entryId": "cursor-bottom-xyz",
								"content": map[string]any{
									"value":      "CURSOR_A",
									"cursorType": "Bottom",
								},
							},
						},
					},
				},
			},
		},
	}
	if got := ExtractBottomCursor(raw); got != "CURSOR_A" {
		t.Errorf("cursor = %q", got)
	}
}

func TestExtractBottomCursorNestedItemContent(t *testing.T) {
	raw := map[string]any{
		"instructions": []any{
			map[string]any{
				"entries": []any{
					map[string]any{
						"entryId": "cursor-bottom-nested",
						"content": map[string]any{
							"itemContent": map[string]any{"value": "CURSOR_B"},
						},
					},
				},
			},
		},
	}
	if got := ExtractBottomCursor(raw); got != "CURSOR_B" {
		t.Errorf("cursor = %q", got)
	}
}

func TestExtractBottomCursorDeterministic(t *testing.T) {
	// A response with TWO sibling branches each containing their own
	// instructions+cursor-bottom. A naive map-iteration DFS would return
	// different cursors on different runs. The sorted BFS walk must return
	// the same cursor every time.
	raw := map[string]any{
		"zebra": map[string]any{
			"instructions": []any{
				map[string]any{
					"entries": []any{
						map[string]any{
							"entryId": "cursor-bottom-z",
							"content": map[string]any{"value": "Z_CURSOR"},
						},
					},
				},
			},
		},
		"alpha": map[string]any{
			"instructions": []any{
				map[string]any{
					"entries": []any{
						map[string]any{
							"entryId": "cursor-bottom-a",
							"content": map[string]any{"value": "A_CURSOR"},
						},
					},
				},
			},
		},
	}
	first := ExtractBottomCursor(raw)
	for i := 0; i < 100; i++ {
		if got := ExtractBottomCursor(raw); got != first {
			t.Fatalf("non-deterministic: run %d returned %q (first was %q)", i, got, first)
		}
	}
	// Alphabetical key sort puts "alpha" before "zebra" in the BFS queue, so
	// the alpha branch's cursor wins.
	if first != "A_CURSOR" {
		t.Errorf("expected A_CURSOR (alphabetical key wins), got %q", first)
	}
}

func TestExtractBottomCursorMissing(t *testing.T) {
	cases := []any{
		nil,
		map[string]any{"data": "none"},
		map[string]any{"instructions": "not-an-array"},
		map[string]any{"instructions": []any{map[string]any{"entries": []any{}}}},
	}
	for i, raw := range cases {
		if got := ExtractBottomCursor(raw); got != "" {
			t.Errorf("case %d: want empty, got %q", i, got)
		}
	}
}
