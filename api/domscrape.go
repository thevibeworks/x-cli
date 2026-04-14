package api

// DOM scraping fallback for endpoints x.com won't serve via direct
// GraphQL calls (Followers and SearchTimeline currently — they require
// the opaque `x-client-transaction-id` header that only the SPA's own
// obfuscated JS knows how to compute).
//
// Same approach XActions' Puppeteer CLI uses: navigate to a real
// x.com page, let the SPA load the content and make its own GraphQL
// calls (including the anti-bot headers), then read the rendered DOM.
// The SPA handles all the fingerprint / CSRF / challenge stuff — we
// just scrape what's already on the screen.
//
// Selectors are ported verbatim from XActions'
// reference/XActions/src/scrapers/twitter/index.js (scrapeFollowers,
// scrapeFollowing, searchTweets). The `data-testid` attributes are
// x.com's own test ids and are stable across UI changes — we've used
// the same ones since 2024. When x.com rebrands and a selector
// breaks, update the JS extractor below and ship.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/thevibeworks/x-cli/internal/chromebrowser"
)

// FollowersDOM scrapes a user's followers by navigating to
// /<user>/followers and reading [data-testid=UserCell] rows from the
// rendered page. Each scroll adds ~20 more rows; scrollCount is
// computed from opts.Limit.
//
// Returns UserSummary records with the fields the DOM exposes:
// username, name, bio, verified, avatar. Follower counts aren't in
// the cell body so they default to 0 — if you need them, run
// `x profile get <username>` for each result, or enable the
// GraphQL path if x.com ever stops requiring x-client-transaction-id
// on Followers.
func (c *Client) FollowersDOM(ctx context.Context, screenName string, opts PageOptions) ([]*UserSummary, error) {
	if c.browser == nil {
		return nil, fmt.Errorf("FollowersDOM: client was not constructed with UseBrowser=true")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	// Each scroll loads ~20 rows. Add a few extra to tolerate the
	// SPA's own dedup and the "UserCell filter" below. 10 extra
	// scrolls is generous; the extractor drops out early once it
	// hits `limit`.
	scrollCount := (limit / 20) + 5

	c.sessionMu.RLock()
	cookies := copyCookies(c.session.Cookies)
	c.sessionMu.RUnlock()

	// Extractor: walks every UserCell on the page, extracts the
	// handle / name / bio / verified / avatar, drops rows with no
	// username or placeholder handles. Ported from XActions
	// scrapeFollowers JS, same selectors.
	extractor := `Array.from(document.querySelectorAll('[data-testid="UserCell"]')).map((cell) => {
		const link = cell.querySelector('a[href^="/"]');
		const nameEl = cell.querySelector('[dir="ltr"] > span');
		const bioEl = cell.querySelector('[data-testid="UserDescription"]');
		const verifiedEl = cell.querySelector('svg[aria-label*="Verified"]');
		const avatarEl = cell.querySelector('img[src*="profile_images"]');
		const href = link ? link.getAttribute('href') : '';
		const username = href.split('/')[1] || '';
		return {
			username: username,
			name: nameEl ? nameEl.textContent : null,
			bio: bioEl ? bioEl.textContent : null,
			verified: !!verifiedEl,
			avatar: avatarEl ? avatarEl.src : null,
		};
	}).filter(u => u.username && !u.username.includes('?'))`

	raw, err := c.browser.Scrape(ctx, chromebrowser.ScrapeOptions{
		URL:          "https://x.com/" + strings.TrimPrefix(screenName, "@") + "/followers",
		WaitSelector: `[data-testid="UserCell"]`,
		Extractor:    extractor,
		ScrollCount:  scrollCount,
		Cookies:      cookies,
	})
	if err != nil {
		return nil, err
	}

	var rows []struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		Bio      string `json:"bio"`
		Verified bool   `json:"verified"`
		Avatar   string `json:"avatar"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("FollowersDOM: decode extractor output: %w", err)
	}

	// Dedup by username — the SPA's virtual scroll sometimes re-renders
	// rows during pagination, so the extractor can see the same row
	// twice across scrolls. First-write-wins.
	seen := make(map[string]struct{}, len(rows))
	out := make([]*UserSummary, 0, limit)
	for _, r := range rows {
		if _, ok := seen[r.Username]; ok {
			continue
		}
		seen[r.Username] = struct{}{}
		out = append(out, &UserSummary{
			Username: r.Username,
			Name:     r.Name,
			Bio:      r.Bio,
			Verified: r.Verified,
			Avatar:   r.Avatar,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// SearchPostsDOM scrapes the Latest results for a search query via
// the rendered /search?q=...&f=live page. Each scroll adds ~20 more
// tweets.
//
// DOM extraction is thinner than ParseTweet: we get the tweet ID,
// author handle, body text, and the raw likes count. Views,
// retweets, quotes, and replies aren't surfaced as easily in the
// compact row layout, so they're left zero. For full metrics, use
// `x tweets get <id>` on each result.
func (c *Client) SearchPostsDOM(ctx context.Context, query string, opts SearchOptions) ([]*Tweet, error) {
	if c.browser == nil {
		return nil, fmt.Errorf("SearchPostsDOM: client was not constructed with UseBrowser=true")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	scrollCount := (limit / 20) + 5

	c.sessionMu.RLock()
	cookies := copyCookies(c.session.Cookies)
	c.sessionMu.RUnlock()

	// Ported verbatim from XActions searchTweets: walks article
	// elements, extracts id from the status URL, text, author handle,
	// and the like count. Same selectors.
	extractor := `Array.from(document.querySelectorAll('article[data-testid="tweet"]')).map((article) => {
		const textEl = article.querySelector('[data-testid="tweetText"]');
		const authorLink = article.querySelector('[data-testid="User-Name"] a[href^="/"]');
		const timeEl = article.querySelector('time');
		const linkEl = article.querySelector('a[href*="/status/"]');
		const likesEl = article.querySelector('[data-testid="like"] span span');
		const idMatch = linkEl && linkEl.href ? linkEl.href.match(/status\/(\d+)/) : null;
		return {
			id: idMatch ? idMatch[1] : null,
			text: textEl ? textEl.textContent : null,
			author: authorLink ? authorLink.href.split('/')[3] : null,
			created_at: timeEl ? timeEl.getAttribute('datetime') : null,
			likes_text: likesEl ? likesEl.textContent : '0',
		};
	}).filter(t => t.id)`

	q := query
	if opts.From != "" {
		q += " from:" + opts.From
	}
	if opts.Since != "" {
		q += " since:" + opts.Since
	}
	if opts.Until != "" {
		q += " until:" + opts.Until
	}
	if opts.Lang != "" {
		q += " lang:" + opts.Lang
	}
	url := "https://x.com/search?q=" + httpQueryEscape(q) + "&src=typed_query&f=live"

	raw, err := c.browser.Scrape(ctx, chromebrowser.ScrapeOptions{
		URL:          url,
		WaitSelector: `article[data-testid="tweet"]`,
		Extractor:    extractor,
		ScrollCount:  scrollCount,
		Cookies:      cookies,
	})
	if err != nil {
		return nil, err
	}

	var rows []struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		Author    string `json:"author"`
		CreatedAt string `json:"created_at"`
		LikesText string `json:"likes_text"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("SearchPostsDOM: decode extractor output: %w", err)
	}

	seen := make(map[string]struct{}, len(rows))
	out := make([]*Tweet, 0, limit)
	for _, r := range rows {
		if r.ID == "" {
			continue
		}
		if _, ok := seen[r.ID]; ok {
			continue
		}
		seen[r.ID] = struct{}{}
		out = append(out, &Tweet{
			ID:        r.ID,
			Text:      r.Text,
			CreatedAt: r.CreatedAt,
			Author:    TweetAuthor{Username: r.Author},
			Metrics:   TweetMetrics{Likes: parseHumanCount(r.LikesText)},
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// copyCookies returns a shallow copy so the extractor doesn't see
// mid-flight mutations while the session's RLock is released.
func copyCookies(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// httpQueryEscape is url.QueryEscape but kept local so this file
// doesn't grow a net/url import just for one call.
func httpQueryEscape(s string) string {
	// Minimal replacements — anything safe enough for an x.com
	// search path. Space → '+' and '#' / '&' encoded are enough for
	// the queries users pass to `x search posts`.
	r := strings.NewReplacer(
		" ", "+",
		"#", "%23",
		"&", "%26",
	)
	return r.Replace(s)
}

// parseHumanCount converts x.com's compact rendered count strings
// ("1.2K", "3.4M", "7,812") back to an integer. Not lossless —
// "1.2K" becomes 1200 — but that matches the precision x.com shows
// in the UI anyway. For exact counts, use the GraphQL path.
func parseHumanCount(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Strip thousands-separator commas for numbers like "7,812".
	s = strings.ReplaceAll(s, ",", "")
	// Check for K/M/B suffix.
	multiplier := 1
	last := s[len(s)-1]
	switch last {
	case 'K', 'k':
		multiplier = 1_000
		s = s[:len(s)-1]
	case 'M', 'm':
		multiplier = 1_000_000
		s = s[:len(s)-1]
	case 'B', 'b':
		multiplier = 1_000_000_000
		s = s[:len(s)-1]
	}
	// Parse the numeric part. Handle "1.2" as 1.2, "12" as 12.
	var whole, frac int
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		whole = atoiOrZero(s)
	} else {
		whole = atoiOrZero(s[:dot])
		fracStr := s[dot+1:]
		if len(fracStr) > 1 {
			fracStr = fracStr[:1]
		}
		frac = atoiOrZero(fracStr)
	}
	return whole*multiplier + (frac*multiplier)/10
}

func atoiOrZero(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
