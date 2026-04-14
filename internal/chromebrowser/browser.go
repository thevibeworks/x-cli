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
	"os"
	"strings"
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
//
// Environment overrides (useful in containers / CI):
//
//	X_CLI_CHROME_PATH  — absolute path to a chromium binary. Required
//	                     when the system Chrome isn't on $PATH (e.g.
//	                     a playwright-installed chromium in
//	                     ~/.cache/ms-playwright/...).
//	X_CLI_CHROME_NO_SANDBOX — set to "1" / "true" to add
//	                          --no-sandbox. Required for headless
//	                          chromium inside an unprivileged
//	                          container.
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
	if p := os.Getenv("X_CLI_CHROME_PATH"); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}
	if v := os.Getenv("X_CLI_CHROME_NO_SANDBOX"); v == "1" || v == "true" {
		opts = append(opts,
			chromedp.NoSandbox,
			chromedp.Flag("disable-dev-shm-usage", true),
		)
	}

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

// Cookies reads the current browser cookie jar for `domain` (e.g.
// "x.com") via CDP Network.GetCookies. Returns a flat name → value
// map. Used by the Transport to expose the jar back to api.Client
// through Set-Cookie response headers so the existing
// client.go mergeSetCookies path folds freshly-issued ct0 / twid /
// gt cookies into the in-memory Session.
//
// Returns an empty map (not an error) when the browser hasn't been
// started yet — the jar is empty, so there's nothing to do.
func (b *Browser) Cookies(ctx context.Context, domain string) (map[string]string, error) {
	b.mu.Lock()
	if !b.started {
		b.mu.Unlock()
		return map[string]string{}, nil
	}
	b.mu.Unlock()

	url := "https://" + domain + "/"
	var raw []*network.Cookie
	err := chromedp.Run(b.ctx, chromedp.ActionFunc(func(c context.Context) error {
		var err error
		raw, err = network.GetCookies().WithURLs([]string{url}).Do(c)
		return err
	}))
	if err != nil {
		return nil, fmt.Errorf("read browser cookie jar: %w", err)
	}
	out := make(map[string]string, len(raw))
	for _, c := range raw {
		out[c.Name] = c.Value
	}
	return out, nil
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

	// We deliberately strip x-csrf-token from the caller-supplied
	// headers because the browser is the source of truth here.
	// The in-page JS below reads `ct0` from document.cookie at fetch
	// time and injects it as x-csrf-token. This is what x.com's own
	// web client does and it's how XActions' Puppeteer path works
	// without the user ever pasting ct0 — the browser fetches a
	// fresh ct0 from x.com via Set-Cookie on the initial navigate
	// and then keeps using whatever the server most recently issued.
	cleanHeaders := make(map[string]string, len(headers))
	for k, v := range headers {
		switch {
		case strings.EqualFold(k, "x-csrf-token"):
			continue
		case strings.EqualFold(k, "Cookie"):
			continue
		default:
			cleanHeaders[k] = v
		}
	}

	headersJSON, err := json.Marshal(cleanHeaders)
	if err != nil {
		return 0, nil, err
	}

	// fetch() must run in an x.com origin so it inherits the cookies
	// and Cloudflare clearance. We navigate to robots.txt first
	// (lightweight, real x.com origin, served by the same Cloudflare
	// edge so any challenge cookies get set as a side effect).
	//
	// The fetch wrapper does three things:
	//   1. Reads the current ct0 from document.cookie. The browser
	//      will have populated this from the navigate's Set-Cookie
	//      response, even if the caller only passed auth_token.
	//   2. Builds the final headers map by merging caller headers
	//      with `x-csrf-token: <fresh ct0>`.
	//   3. Sends the request with credentials:'include' so every
	//      jar cookie (auth_token, ct0, gt, _twitter_sess, ...)
	//      goes along.
	js := fmt.Sprintf(`
		(async () => {
			try {
				const ct0 = (document.cookie.match(/(?:^|;\s*)ct0=([^;]+)/) || [])[1] || '';
				const headers = Object.assign({}, %s);
				if (ct0) headers['x-csrf-token'] = ct0;
				const r = await fetch(%q, {
					method: %q,
					credentials: 'include',
					headers: headers,
				});
				const body = await r.text();
				return JSON.stringify({status: r.status, body: body, ct0_present: !!ct0});
			} catch (e) {
				return JSON.stringify({status: 0, body: 'browser fetch error: ' + (e && e.message)});
			}
		})()
	`, string(headersJSON), url, method)

	// Navigate to /i/release_notes — lightweight x.com page that
	// gives us the auth_token+ct0+twid bootstrap without paying for
	// a full SPA hydrate. We tested deeper pages (/home) and they
	// don't change the per-op behavior — Followers/SearchTimeline
	// 404 has a different cause than the navigate target.
	var raw string
	err = chromedp.Run(b.ctx,
		network.SetCookies(cookieParams),
		chromedp.Navigate("https://x.com/i/release_notes"),
		chromedp.Evaluate(js, &raw, chromedp.EvalAsValue, withAwaitPromise),
	)
	if err != nil {
		return 0, nil, fmt.Errorf("chromedp run: %w", err)
	}

	var resp struct {
		Status     int    `json:"status"`
		Body       string `json:"body"`
		Ct0Present bool   `json:"ct0_present"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return 0, nil, fmt.Errorf("decode browser fetch result: %w", err)
	}
	if !resp.Ct0Present {
		// Diagnostic: if x.com didn't issue a ct0 cookie, the
		// upstream call will fail csrf-mismatch. Surface the cause
		// in the body so callers can render a better error than
		// "rejected session".
		resp.Body = `{"errors":[{"message":"x.com did not return a ct0 cookie on navigation. Verify the auth_token is valid and Chrome reached x.com without a Cloudflare interstitial."}]}`
		if resp.Status == 0 {
			resp.Status = 401
		}
	}
	return resp.Status, []byte(resp.Body), nil
}

// withAwaitPromise tells chromedp.Evaluate that the JS expression
// returns a Promise that should be awaited before returning.
func withAwaitPromise(p *runtime.EvaluateParams) *runtime.EvaluateParams {
	return p.WithAwaitPromise(true)
}
