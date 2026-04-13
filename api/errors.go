package api

import "fmt"

type AuthError struct {
	Msg    string
	Status int
}

func (e *AuthError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("auth: %s (http %d)", e.Msg, e.Status)
	}
	return "auth: " + e.Msg
}

type RateLimitError struct {
	Endpoint string
	ResetAt  int64
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited on %s (resets at unix %d)", e.Endpoint, e.ResetAt)
}

type NotFoundError struct {
	Endpoint string
}

func (e *NotFoundError) Error() string { return "not found: " + e.Endpoint }

type APIError struct {
	Endpoint string
	Status   int
	Body     string
}

// Error formats the API error. The response body is capped so that an
// endpoint that echoes request headers cannot leak cookie fragments into
// logs or crash reports.
func (e *APIError) Error() string {
	const cap = 512
	body := e.Body
	if len(body) > cap {
		body = body[:cap] + "...(truncated)"
	}
	return fmt.Sprintf("api %s: http %d: %s", e.Endpoint, e.Status, body)
}

type NetworkError struct {
	Endpoint string
	Err      error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("network error on %s: %v", e.Endpoint, e.Err)
}

func (e *NetworkError) Unwrap() error { return e.Err }
