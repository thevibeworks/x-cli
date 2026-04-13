package api

import (
	"errors"
	"strings"
	"testing"
)

func TestAuthErrorMessage(t *testing.T) {
	e := &AuthError{Msg: "no auth_token"}
	if !strings.Contains(e.Error(), "no auth_token") {
		t.Errorf("error = %q", e.Error())
	}
	withStatus := &AuthError{Msg: "bad session", Status: 401}
	if !strings.Contains(withStatus.Error(), "401") {
		t.Errorf("expected status in message, got %q", withStatus.Error())
	}
}

func TestRateLimitErrorMessage(t *testing.T) {
	e := &RateLimitError{Endpoint: "UserTweets", ResetAt: 1234567890}
	msg := e.Error()
	if !strings.Contains(msg, "UserTweets") {
		t.Errorf("endpoint missing from %q", msg)
	}
	if !strings.Contains(msg, "1234567890") {
		t.Errorf("resetAt missing from %q", msg)
	}
}

func TestNotFoundErrorMessage(t *testing.T) {
	e := &NotFoundError{Endpoint: "Op"}
	if !strings.Contains(e.Error(), "Op") {
		t.Errorf("error = %q", e.Error())
	}
}

func TestAPIErrorMessage(t *testing.T) {
	e := &APIError{Endpoint: "Op", Status: 500, Body: "oops"}
	msg := e.Error()
	for _, want := range []string{"Op", "500", "oops"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in %q", want, msg)
		}
	}
}

func TestNetworkErrorUnwrap(t *testing.T) {
	inner := errors.New("dial refused")
	e := &NetworkError{Endpoint: "Op", Err: inner}
	if !errors.Is(e, inner) {
		t.Error("NetworkError should unwrap")
	}
	if !strings.Contains(e.Error(), "dial refused") {
		t.Errorf("error = %q", e.Error())
	}
}

func TestBudgetExhaustedErrorMessage(t *testing.T) {
	e := &BudgetExhaustedError{Cap: 200}
	if !strings.Contains(e.Error(), "budget") {
		t.Errorf("error = %q", e.Error())
	}
}
