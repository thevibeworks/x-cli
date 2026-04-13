// Package chromebrowser drives a real headless Chrome via the
// DevTools Protocol (chromedp) so x-cli can talk to x.com endpoints
// that sit behind Cloudflare Bot Management. This is the same
// approach XActions' CLI uses (Puppeteer + puppeteer-extra-stealth):
// instead of fighting Cloudflare's TLS / HTTP/2 / JS fingerprint
// checks one by one, just BE Chrome.
//
// Trade-offs vs the http+utls path:
//
//   + Works against Cloudflare Bot Management, JS challenges, HTTP/2
//     fingerprinting, and any other anti-bot heuristic Cloudflare or
//     X's edge ships in the future.
//   - Requires Chrome to be installed (chromedp launches the system
//     Chrome by default).
//   - First call pays a ~1-2s Chrome startup cost. Subsequent calls
//     reuse the same Browser instance and run in ~200-500ms.
//   - ~10MB of extra deps (chromedp + cdproto, both pure Go).
//
// The intended UX: try this transport first; on failure (Chrome not
// installed, sandbox error, headless detection) fall back to
// http+utls. See cmd/auth.go runAuthImport for the auto-fallback.
package chromebrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Browser holds a long-lived headless Chrome instance and exposes a
// Fetch method that runs `fetch()` inside a real browser context.
//
// Concurrent calls to Fetch serialize on the underlying chromedp
// context — chromedp handles its own message routing per CDP target,
// but x-cli only ever needs serial requests so we keep the locking
// simple. Close releases the Chrome process.
type Browser struct {
	mu sync.Mutex

	allocCancel context.CancelFunc
	ctxCancel   context.CancelFunc
	ctx         context.Context
	started     bool
}

// New returns an unstarted Browser. The actual Chrome process is
// spawned lazily on the first Fetch call so a Browser that never
// fires a request costs nothing.
func New() *Browser { return &Browser{} }

// start launches the Chrome process. Idempotent — safe to call from
// multiple Fetch entries; second-and-later calls are no-ops.
func (b *Browser) start() error {
	if b.started {
		return nil
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		// "new" headless mode (Chrome 109+) is much harder to
		// fingerprint than the legacy --headless flag. Bot management
		// systems still detect it, but we add manual stealth flags
		// below for the rest.
		chromedp.Flag("headless", "new"),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-features", "AutomationControlled,IsolateOrigins,site-per-process"),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("no-first-run", true),
		chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
		chromedp.WindowSize(1280, 800),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	b.allocCancel = allocCancel

	bctx, bcancel := chromedp.NewContext(allocCtx)
	b.ctx = bctx
	b.ctxCancel = bcancel

	// Pre-warm the browser by running an empty action set. This
	// surfaces "Chrome not installed" errors at start time rather
	// than on the first Fetch.
	if err := chromedp.Run(bctx); err != nil {
		bcancel()
		allocCancel()
		return fmt.Errorf("launch chrome (is it installed and in $PATH?): %w", err)
	}
	b.started = true
	return nil
}

// Close terminates the Chrome process. Safe to call on a never-started
// Browser. Calling Fetch after Close panics — re-create the Browser.
func (b *Browser) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.started {
		return
	}
	if b.ctxCancel != nil {
		b.ctxCancel()
	}
	if b.allocCancel != nil {
		b.allocCancel()
	}
	b.started = false
}

// Fetch performs an HTTP request via the headless Chrome instance.
// The request is fired by `fetch()` inside an x.com page context, so
// it inherits Chrome's TLS handshake, HTTP/2 frame ordering, and
// any cf_clearance cookies Cloudflare sets along the way.
//
// Cookies are pushed into the browser's cookie store via CDP
// `Network.SetCookies` BEFORE the page navigation. Headers are
// applied to the fetch options inside the page.
//
// Returns the HTTP status, the response body bytes, and any
// transport error. Caller is responsible for parsing the body.
func (b *Browser) Fetch(ctx context.Context, method, url string, headers, cookies map[string]string) (status int, body []byte, err error) {
	b.mu.Lock()
	if err := b.start(); err != nil {
		b.mu.Unlock()
		return 0, nil, err
	}
	b.mu.Unlock()

	cookieParams := make([]*network.CookieParam, 0, len(cookies))
	for name, value := range cookies {
		if value == "" {
			continue
		}
		cookieParams = append(cookieParams, &network.CookieParam{
			Name:   name,
			Value:  value,
			Domain: ".x.com",
			Path:   "/",
			Secure: true,
		})
	}

	headersJSON, err := json.Marshal(headers)
	if err != nil {
		return 0, nil, err
	}

	// fetch() must run in an x.com origin so it inherits the cookies
	// and Cloudflare clearance. We navigate to robots.txt first
	// (lightweight, real x.com origin, served by the same Cloudflare
	// edge so any challenge cookies get set as a side effect).
	js := fmt.Sprintf(`
		(async () => {
			try {
				const r = await fetch(%q, {
					method: %q,
					credentials: 'include',
					headers: %s,
				});
				const body = await r.text();
				return JSON.stringify({status: r.status, body: body});
			} catch (e) {
				return JSON.stringify({status: 0, body: 'browser fetch error: ' + (e && e.message)});
			}
		})()
	`, url, method, string(headersJSON))

	var raw string
	err = chromedp.Run(b.ctx,
		network.SetCookies(cookieParams),
		chromedp.Navigate("https://x.com/robots.txt"),
		chromedp.Evaluate(js, &raw, chromedp.EvalAsValue, withAwaitPromise),
	)
	if err != nil {
		return 0, nil, fmt.Errorf("chromedp run: %w", err)
	}

	var resp struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return 0, nil, fmt.Errorf("decode browser fetch result: %w", err)
	}
	return resp.Status, []byte(resp.Body), nil
}

// withAwaitPromise tells chromedp.Evaluate that the JS expression
// returns a Promise that should be awaited before returning.
func withAwaitPromise(p *runtime.EvaluateParams) *runtime.EvaluateParams {
	return p.WithAwaitPromise(true)
}
