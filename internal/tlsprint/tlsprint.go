// Package tlsprint provides an http.Transport that performs the TLS
// handshake using uTLS with a Chrome 120 ClientHelloID. x.com's
// /i/api/graphql/* path is fronted by Cloudflare Bot Management, and
// Cloudflare matches on the TLS ClientHello fingerprint (JA3/JA4).
// Go's stdlib net/http has a distinctive JA3 that Cloudflare flags as
// "non-browser" and serves a challenge page instead of the real
// response. Impersonating Chrome at the TLS layer is how we get past.
//
// Scope: TLS handshake only. We deliberately pin ALPN to http/1.1 so
// Go's stdlib handles the HTTP layer. Chrome actually negotiates h2 in
// ALPN, which means Cloudflare sees our http/1.1-only advertisement as
// slightly-odd-but-not-suspicious. If a future commit needs full h2
// fingerprint parity, `github.com/bogdanfinn/tls-client` is the next
// escalation.
//
// Plain HTTP connections (httptest servers in unit tests) go through
// the standard DialContext path, so the transport is a drop-in for
// local testing too.
package tlsprint

import (
	"context"
	"net"
	"net/http"
	"time"

	utls "github.com/refraction-networking/utls"
)

// NewChromeTransport returns an *http.Transport wired to impersonate
// Chrome 120 for HTTPS connections. Safe to reuse across requests
// and goroutines (http.Transport is concurrency-safe).
func NewChromeTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		DialContext:           dialer.DialContext,
		DialTLSContext:        dialTLSWithUTLS(dialer, utls.HelloChrome_120),
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// dialTLSWithUTLS returns a DialTLSContext hook that performs a uTLS
// handshake with the given ClientHelloID.
func dialTLSWithUTLS(dialer *net.Dialer, helloID utls.ClientHelloID) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			raw.Close()
			return nil, err
		}
		config := &utls.Config{
			ServerName: host,
			// Force h1 so Go's stdlib handles the HTTP layer. Chrome
			// actually advertises `h2, http/1.1` in ALPN; we don't
			// match that exactly, but JA3/JA4 is what Cloudflare's
			// bot-management layer checks first and matching the Hello
			// itself is the load-bearing part.
			NextProtos: []string{"http/1.1"},
		}
		uconn := utls.UClient(raw, config, helloID)
		if err := uconn.HandshakeContext(ctx); err != nil {
			uconn.Close()
			return nil, err
		}
		return uconn, nil
	}
}
