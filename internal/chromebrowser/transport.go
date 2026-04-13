package chromebrowser

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Transport is an http.RoundTripper that routes every request through
// a headless Chrome instance via chromedp. Use it as the Transport
// field of an http.Client and the rest of x-cli's HTTP code (api/client.go,
// retries, throttle, etc.) keeps working unchanged.
//
// Cookies and headers from the *http.Request are forwarded into the
// browser fetch. The Cookie header is split out and pushed via
// Network.SetCookies (CDP) — the in-page fetch then automatically
// includes them via `credentials: 'include'`.
type Transport struct {
	browser *Browser
}

// NewTransport returns a Transport with an unstarted Browser. The
// Chrome process is launched lazily on the first request.
func NewTransport() *Transport {
	return &Transport{browser: New()}
}

// Close releases the Chrome process. Safe to call multiple times.
func (t *Transport) Close() {
	if t.browser != nil {
		t.browser.Close()
	}
}

// RoundTrip implements http.RoundTripper. The body of the inbound
// request is currently ignored — x-cli only uses the browser path
// for GET requests today (GraphQL via query string). When we need
// POST bodies we'll add a `body` arg to Browser.Fetch and pipe it
// through the JS fetch options.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	headers := map[string]string{}
	cookies := map[string]string{}

	for k, vs := range req.Header {
		if len(vs) == 0 {
			continue
		}
		// Cookies go through CDP, not the fetch headers — the
		// browser's own cookie store applies them via
		// `credentials: 'include'`.
		if strings.EqualFold(k, "Cookie") {
			continue
		}
		headers[k] = vs[0]
	}
	for _, c := range req.Cookies() {
		cookies[c.Name] = c.Value
	}

	status, body, err := t.browser.Fetch(req.Context(), req.Method, req.URL.String(), headers, cookies)
	if err != nil {
		return nil, err
	}

	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Body:          io.NopCloser(bytes.NewReader(body)),
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Request:       req,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		ContentLength: int64(len(body)),
	}, nil
}
