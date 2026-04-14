package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFollowUserSendsCorrectFormBody(t *testing.T) {
	var captured map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = map[string]string{
			"path":   r.URL.Path,
			"method": r.Method,
			"body":   string(body),
			"ctype":  r.Header.Get("Content-Type"),
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": 1, "name": "test"}`))
	}))
	defer srv.Close()

	eps := &EndpointMap{
		Bases:  Bases{REST: srv.URL, GraphQL: srv.URL},
		Bearer: "B",
		REST: map[string]RESTEndpoint{
			"friendshipsCreate": {
				Path:     "/1.1/friendships/create.json",
				Method:   "POST",
				Kind:     "mutation",
				MinGap:   10 * 1000 * 1000, // 10ms
				MaxGap:   10 * 1000 * 1000,
				DailyCap: 10,
			},
		},
	}
	c := New(Options{
		Endpoints: eps,
		Throttle:  NewThrottle(Defaults{}),
		Session:   Session{Cookies: map[string]string{"auth_token": "x", "ct0": "y"}},
	})

	if err := c.FollowUser(context.Background(), "12345"); err != nil {
		t.Fatal(err)
	}

	if captured["path"] != "/1.1/friendships/create.json" {
		t.Errorf("path = %q", captured["path"])
	}
	if captured["method"] != "POST" {
		t.Errorf("method = %q", captured["method"])
	}
	if !strings.HasPrefix(captured["ctype"], "application/x-www-form-urlencoded") {
		t.Errorf("content-type = %q", captured["ctype"])
	}
	for _, want := range []string{"user_id=12345", "skip_status=true", "include_profile_interstitial_type=1"} {
		if !strings.Contains(captured["body"], want) {
			t.Errorf("body %q missing %q", captured["body"], want)
		}
	}
}

func TestFollowUserEmptyIDReturnsError(t *testing.T) {
	c := &Client{}
	if err := c.FollowUser(context.Background(), ""); err == nil {
		t.Error("expected error for empty user id")
	}
}

// TestGraphqlMutationLikeIdempotent runs the like path against a fake
// server that returns the "you have already favorited" envelope X
// emits on a re-like. The Client.LikeTweet wrapper must treat that as
// success (return nil), not as an error.
func TestGraphqlMutationLikeIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":null,"errors":[{"code":139,"message":"You have already favorited this tweet."}]}`))
	}))
	defer srv.Close()

	eps := &EndpointMap{
		Bases:  Bases{REST: srv.URL, GraphQL: srv.URL},
		Bearer: "B",
		GraphQL: map[string]GraphQLEndpoint{
			"FavoriteTweet": {QueryID: "fav_qid", OperationName: "FavoriteTweet", Kind: "mutation", RPS: 100, Burst: 10},
		},
	}
	c := New(Options{
		Endpoints: eps,
		Throttle:  NewThrottle(Defaults{}),
		Session:   Session{Cookies: map[string]string{"auth_token": "x", "ct0": "y"}},
	})

	if err := c.LikeTweet(context.Background(), "12345"); err != nil {
		t.Errorf("LikeTweet should succeed on 'already favorited', got %v", err)
	}
}

// TestGraphqlMutationLikeRateLimited verifies that a GraphQL mutation
// rate-limit envelope maps to *RateLimitError, not a generic APIError.
func TestGraphqlMutationLikeRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":null,"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
	}))
	defer srv.Close()

	eps := &EndpointMap{
		Bases:  Bases{GraphQL: srv.URL},
		Bearer: "B",
		GraphQL: map[string]GraphQLEndpoint{
			"FavoriteTweet": {QueryID: "fav_qid", OperationName: "FavoriteTweet", Kind: "mutation", RPS: 100, Burst: 10},
		},
	}
	c := New(Options{
		Endpoints: eps,
		Throttle:  NewThrottle(Defaults{}),
		Session:   Session{Cookies: map[string]string{"auth_token": "x", "ct0": "y"}},
	})

	err := c.LikeTweet(context.Background(), "12345")
	if err == nil {
		t.Fatal("want rate-limit error, got nil")
	}
	var rate *RateLimitError
	if !errors.As(err, &rate) {
		t.Errorf("want *RateLimitError, got %T: %v", err, err)
	}
}

func TestClassifyMutationErrors(t *testing.T) {
	cases := []struct {
		name    string
		body    map[string]any
		wantErr error
	}{
		{
			name:    "no errors",
			body:    map[string]any{},
			wantErr: nil,
		},
		{
			name: "already following",
			body: map[string]any{
				"errors": []any{
					map[string]any{"message": "You have already followed this user."},
				},
			},
			wantErr: nil, // idempotent
		},
		{
			name: "rate limited",
			body: map[string]any{
				"errors": []any{
					map[string]any{"message": "Rate limit exceeded"},
				},
			},
			wantErr: &RateLimitError{},
		},
		{
			name: "user not found",
			body: map[string]any{
				"errors": []any{
					map[string]any{"message": "Cannot find specified user"},
				},
			},
			wantErr: &NotFoundError{},
		},
		{
			name: "spam protection",
			body: map[string]any{
				"errors": []any{
					map[string]any{"message": "To protect our users from spam"},
				},
			},
			wantErr: &RateLimitError{},
		},
		{
			name: "unknown error",
			body: map[string]any{
				"errors": []any{
					map[string]any{"message": "Some unrecognised error"},
				},
			},
			wantErr: &APIError{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyMutationErrors("test", tc.body)
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("want nil, got %T: %v", err, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want %T, got nil", tc.wantErr)
			}
			switch tc.wantErr.(type) {
			case *RateLimitError:
				var target *RateLimitError
				if !errors.As(err, &target) {
					t.Errorf("want *RateLimitError, got %T: %v", err, err)
				}
			case *NotFoundError:
				var target *NotFoundError
				if !errors.As(err, &target) {
					t.Errorf("want *NotFoundError, got %T: %v", err, err)
				}
			case *APIError:
				var target *APIError
				if !errors.As(err, &target) {
					t.Errorf("want *APIError, got %T: %v", err, err)
				}
			}
		})
	}
}
