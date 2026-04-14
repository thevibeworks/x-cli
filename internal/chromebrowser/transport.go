package chromebrowser

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
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
//
// After the fetch completes, RoundTrip reads the browser's cookie
// jar back via Browser.Cookies and surfaces every cookie as a
// `Set-Cookie` header on the response. The api.Client's existing
// mergeSetCookies path then folds them into the in-memory Session.
// This is how the upstream caller learns about ct0 / twid / gt
// cookies that x.com Set-Cookie'd during the initial navigate
// without the user ever pasting them.
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

	respHeader := http.Header{"Content-Type": []string{"application/json"}}

	// Read the browser jar back so the api.Client can merge any
	// freshly-issued cookies (ct0, twid, gt, ...) into its session.
	// Failing to read the jar is non-fatal; we still return the
	// upstream response, the caller just won't learn about new
	// cookies.
	if jar, jarErr := t.browser.Cookies(req.Context(), "x.com"); jarErr == nil {
		names := make([]string, 0, len(jar))
		for name, value := range jar {
			if name == "" || value == "" {
				continue
			}
			names = append(names, name)
			respHeader.Add("Set-Cookie",
				fmt.Sprintf("%s=%s; Domain=.x.com; Path=/; Secure", name, value))
		}
		if os.Getenv("X_CLI_BROWSER_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "» chromebrowser: jar after fetch has %d cookie(s): %v\n", len(names), names)
		}
	}

	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Body:          io.NopCloser(bytes.NewReader(body)),
		Header:        respHeader,
		Request:       req,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		ContentLength: int64(len(body)),
	}, nil
}
