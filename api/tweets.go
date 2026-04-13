package api

// Tweet scraping. Mirrors XActions' src/scrapers/twitter/http/tweets.js
// but rewritten in Go with typed projections, the stable rules below, and
// integrated with the x-cli throttle and Set-Cookie rotation in client.go.
//
// Stable rules x-cli inherits from XActions and you should not regress:
//
//  1. Recognize three tweet shapes from the GraphQL gateway:
//
//       __typename: "Tweet"                         — vanilla
//       __typename: "TweetWithVisibilityResults"    — wrap one level deeper
//       __typename: "TweetTombstone"                — deleted/withheld
//
//  2. Quote tweets and retweets are nested tweet objects — recursively
//     project them with the same parser.
//
//  3. Timeline responses dispatch on instruction type:
//
//       TimelineAddEntries     — primary entries + cursor
//       TimelineAddToModule    — conversation modules (UserTweetsAndReplies)
//       TimelinePinEntry       — pinned tweet
//
//  4. Entry IDs are typed by prefix: "tweet-", "user-", "cursor-bottom-",
//     "cursor-top-".
//
//  5. Pick the highest-bitrate `video/mp4` variant for video URLs.
//
//  6. Pagination loop ends when the response yields no bottom cursor or
//     no new entries — never on a fixed page count.

import (
	"context"
	"sort"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// Domain types
// -----------------------------------------------------------------------------

// Tweet is the projected, stable view of an X tweet. It is a strict subset
// of the GraphQL response — fields the CLI actually renders.
type Tweet struct {
	ID         string        `json:"id"`
	Text       string        `json:"text"`
	CreatedAt  string        `json:"created_at"` // ISO-8601
	Author     TweetAuthor   `json:"author"`
	Metrics    TweetMetrics  `json:"metrics"`
	Media      []TweetMedia  `json:"media,omitempty"`
	Quoted     *Tweet        `json:"quoted,omitempty"`
	InReplyTo  *ReplyContext `json:"in_reply_to,omitempty"`
	URLs       []TweetURL    `json:"urls,omitempty"`
	Hashtags   []string      `json:"hashtags,omitempty"`
	Mentions   []TweetMention `json:"mentions,omitempty"`
	Lang       string        `json:"lang,omitempty"`
	Source     string        `json:"source,omitempty"`
	IsReply    bool          `json:"is_reply,omitempty"`
	IsRetweet  bool          `json:"is_retweet,omitempty"`
	RetweetOf  *Tweet        `json:"retweet_of,omitempty"`
	Tombstone  bool          `json:"tombstone,omitempty"`
}

type TweetAuthor struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Verified bool   `json:"verified"`
	Avatar   string `json:"avatar,omitempty"`
}

type TweetMetrics struct {
	Likes     int `json:"likes"`
	Retweets  int `json:"retweets"`
	Replies   int `json:"replies"`
	Quotes    int `json:"quotes"`
	Bookmarks int `json:"bookmarks"`
	Views     int `json:"views"`
}

type TweetMedia struct {
	Type     string `json:"type"` // photo | video | animated_gif
	URL      string `json:"url"`
	VideoURL string `json:"video_url,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

type ReplyContext struct {
	TweetID  string `json:"tweet_id"`
	UserID   string `json:"user_id,omitempty"`
	Username string `json:"username,omitempty"`
}

type TweetURL struct {
	URL         string `json:"url"`
	ExpandedURL string `json:"expanded_url,omitempty"`
	DisplayURL  string `json:"display_url,omitempty"`
}

type TweetMention struct {
	Username string `json:"username"`
	ID       string `json:"id,omitempty"`
}

// Thread is a reconstructed conversation, sorted chronologically.
type Thread struct {
	Root         *Tweet  `json:"root"`
	Tweets       []*Tweet `json:"tweets"`
	TotalReplies int     `json:"total_replies"`
}

// -----------------------------------------------------------------------------
// Parser — ParseTweet (mirrors parseTweetData in XActions)
// -----------------------------------------------------------------------------

// maxQuoteDepth caps recursion through quoted/retweeted nested tweets.
// Real X responses rarely nest beyond 2-3 levels (a quote of a quote);
// this guards against pathological or malicious responses that could
// otherwise drive the parser into stack exhaustion.
const maxQuoteDepth = 5

// ParseTweet projects a raw GraphQL tweet result into a Tweet. Returns nil
// on a nil/non-map input so callers can use `if t := ParseTweet(raw); t != nil`.
//
// Recognizes:
//   - "Tweet"
//   - "TweetWithVisibilityResults" (unwrap one level)
//   - "TweetTombstone" (returns a non-nil tombstone marker with ID="")
func ParseTweet(raw any) *Tweet {
	return parseTweetDepth(raw, 0)
}

func parseTweetDepth(raw any, depth int) *Tweet {
	if depth > maxQuoteDepth {
		return nil
	}
	rawMap, ok := raw.(map[string]any)
	if !ok || rawMap == nil {
		return nil
	}

	typename := getString(rawMap, "__typename")
	if typename == "TweetTombstone" {
		text := "[Unavailable]"
		if t := walkPathMap(rawMap, "tombstone", "text"); t != nil {
			if s := getString(t, "text"); s != "" {
				text = s
			}
		}
		return &Tweet{Text: text, Tombstone: true}
	}

	// TweetWithVisibilityResults wraps the real tweet one level deeper.
	tweet := rawMap
	if typename == "TweetWithVisibilityResults" {
		if inner := getMap(rawMap, "tweet"); inner != nil {
			tweet = inner
		}
	}

	legacy := getMap(tweet, "legacy")
	core := getMap(tweet, "core")

	// ---- Author -----------------------------------------------------------
	authorResult := walkPathMap(core, "user_results", "result")
	authorLegacy := getMap(authorResult, "legacy")
	author := TweetAuthor{
		ID:       getString(authorResult, "rest_id"),
		Username: getString(authorLegacy, "screen_name"),
		Name:     getString(authorLegacy, "name"),
		Avatar:   getString(authorLegacy, "profile_image_url_https"),
		Verified: getBool(authorResult, "is_blue_verified") || getBool(authorLegacy, "verified"),
	}

	// ---- Metrics ----------------------------------------------------------
	metrics := TweetMetrics{
		Likes:     getInt(legacy, "favorite_count"),
		Retweets:  getInt(legacy, "retweet_count"),
		Replies:   getInt(legacy, "reply_count"),
		Quotes:    getInt(legacy, "quote_count"),
		Bookmarks: getInt(legacy, "bookmark_count"),
	}
	if views := getMap(tweet, "views"); views != nil {
		metrics.Views = getInt(views, "count")
	} else if extViews := getMap(tweet, "ext_views"); extViews != nil {
		metrics.Views = getInt(extViews, "count")
	}

	// ---- Media ------------------------------------------------------------
	rawMedia := walkPathSlice(legacy, "extended_entities", "media")
	media := make([]TweetMedia, 0, len(rawMedia))
	for _, m := range rawMedia {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		original := getMap(mm, "original_info")
		entry := TweetMedia{
			Type:   firstNonEmpty(getString(mm, "type"), "photo"),
			URL:    firstNonEmpty(getString(mm, "media_url_https"), getString(mm, "media_url")),
			Width:  getInt(original, "width"),
			Height: getInt(original, "height"),
		}
		if vi := getMap(mm, "video_info"); vi != nil {
			entry.VideoURL = pickBestVideoURL(getSlice(vi, "variants"))
		}
		media = append(media, entry)
	}

	// ---- Quoted tweet (recursive, depth-capped) ---------------------------
	var quoted *Tweet
	if qResult := walkPathMap(tweet, "quoted_status_result", "result"); qResult != nil {
		quoted = parseTweetDepth(qResult, depth+1)
	}

	// ---- Reply context ----------------------------------------------------
	var inReplyTo *ReplyContext
	if rid := getString(legacy, "in_reply_to_status_id_str"); rid != "" {
		inReplyTo = &ReplyContext{
			TweetID:  rid,
			UserID:   getString(legacy, "in_reply_to_user_id_str"),
			Username: getString(legacy, "in_reply_to_screen_name"),
		}
	}

	// ---- URLs / hashtags / mentions ---------------------------------------
	urls := []TweetURL{}
	for _, u := range walkPathSlice(legacy, "entities", "urls") {
		um, ok := u.(map[string]any)
		if !ok {
			continue
		}
		urls = append(urls, TweetURL{
			URL:         getString(um, "url"),
			ExpandedURL: getString(um, "expanded_url"),
			DisplayURL:  getString(um, "display_url"),
		})
	}
	hashtags := []string{}
	for _, h := range walkPathSlice(legacy, "entities", "hashtags") {
		if hm, ok := h.(map[string]any); ok {
			if s := getString(hm, "text"); s != "" {
				hashtags = append(hashtags, s)
			}
		}
	}
	mentions := []TweetMention{}
	for _, m := range walkPathSlice(legacy, "entities", "user_mentions") {
		if mm, ok := m.(map[string]any); ok {
			mentions = append(mentions, TweetMention{
				Username: getString(mm, "screen_name"),
				ID:       getString(mm, "id_str"),
			})
		}
	}

	// ---- Retweet (recursive, depth-capped) --------------------------------
	var (
		isRetweet bool
		retweetOf *Tweet
	)
	if rt := walkPathMap(legacy, "retweeted_status_result", "result"); rt != nil {
		retweetOf = parseTweetDepth(rt, depth+1)
		isRetweet = true
	}

	id := getString(tweet, "rest_id")
	if id == "" {
		id = getString(legacy, "id_str")
	}

	return &Tweet{
		ID:        id,
		Text:      getString(legacy, "full_text"),
		CreatedAt: parseTwitterDate(getString(legacy, "created_at")),
		Author:    author,
		Metrics:   metrics,
		Media:     media,
		Quoted:    quoted,
		InReplyTo: inReplyTo,
		URLs:      urls,
		Hashtags:  hashtags,
		Mentions:  mentions,
		Lang:      getString(legacy, "lang"),
		Source:    stripHTMLTags(getString(legacy, "source")),
		IsReply:   inReplyTo != nil,
		IsRetweet: isRetweet,
		RetweetOf: retweetOf,
	}
}

// -----------------------------------------------------------------------------
// Timeline instruction parser
// -----------------------------------------------------------------------------

// ParseTimelineInstructions walks a GraphQL `instructions` array and returns
// every tweet it can parse, plus the bottom cursor for pagination.
//
// Handles:
//   - TimelineAddEntries — top-level entries (most common)
//   - TimelineAddToModule — conversation modules (UserTweetsAndReplies)
//   - TimelinePinEntry — pinned tweet
func ParseTimelineInstructions(insts []any) (tweets []*Tweet, cursor string) {
	for _, inst := range insts {
		im, ok := inst.(map[string]any)
		if !ok {
			continue
		}
		switch getString(im, "type") {
		case "TimelineAddEntries":
			for _, e := range getSlice(im, "entries") {
				em, ok := e.(map[string]any)
				if !ok {
					continue
				}
				id := getString(em, "entryId")
				if strings.HasPrefix(id, "cursor-bottom-") {
					if c := extractCursorValue(em); c != "" {
						cursor = c
					}
					continue
				}
				if strings.HasPrefix(id, "cursor-top-") {
					continue
				}
				if t := extractTweetFromEntry(em); t != nil {
					tweets = append(tweets, t)
				}
				// Conversation module items inline in an entry (UserTweetsAndReplies).
				for _, mi := range walkPathSlice(em, "content", "items") {
					if mt := extractTweetFromModuleItem(mi); mt != nil {
						tweets = append(tweets, mt)
					}
				}
			}
		case "TimelineAddToModule":
			for _, mi := range getSlice(im, "moduleItems") {
				if t := extractTweetFromModuleItem(mi); t != nil {
					tweets = append(tweets, t)
				}
			}
		case "TimelinePinEntry":
			if entry := getMap(im, "entry"); entry != nil {
				if t := extractTweetFromEntry(entry); t != nil {
					tweets = append(tweets, t)
				}
			}
		}
	}
	return tweets, cursor
}

func extractTweetFromEntry(entry map[string]any) *Tweet {
	res := walkPathMap(entry, "content", "itemContent", "tweet_results", "result")
	if res == nil {
		return nil
	}
	t := ParseTweet(res)
	if t == nil || t.ID == "" {
		return nil
	}
	return t
}

func extractTweetFromModuleItem(item any) *Tweet {
	im, ok := item.(map[string]any)
	if !ok {
		return nil
	}
	res := walkPathMap(im, "item", "itemContent", "tweet_results", "result")
	if res == nil {
		return nil
	}
	t := ParseTweet(res)
	if t == nil || t.ID == "" {
		return nil
	}
	return t
}

func extractCursorValue(entry map[string]any) string {
	content := getMap(entry, "content")
	if v := getString(content, "value"); v != "" {
		return v
	}
	if ic := getMap(content, "itemContent"); ic != nil {
		if v := getString(ic, "value"); v != "" {
			return v
		}
	}
	return ""
}

// -----------------------------------------------------------------------------
// Scraping API — public methods on *Client
// -----------------------------------------------------------------------------

// TimelineOptions configures a paginated tweet timeline scrape.
type TimelineOptions struct {
	Limit          int
	Cursor         string
	IncludeReplies bool
	OnPage         func(fetched, limit int)
}

// UserTweets scrapes a user's own tweets via the UserTweets GraphQL endpoint.
// Resolves the screen name to a numeric user ID via UserByScreenName first.
func (c *Client) UserTweets(ctx context.Context, screenName string, opts TimelineOptions) ([]*Tweet, error) {
	if opts.IncludeReplies {
		return c.UserTweetsAndReplies(ctx, screenName, opts)
	}
	userID, err := c.resolveUserID(ctx, screenName)
	if err != nil {
		return nil, err
	}
	return c.scrapeUserTimeline(ctx, "UserTweets", map[string]any{
		"userId":                                  userID,
		"count":                                   20,
		"includePromotedContent":                  false,
		"withQuickPromoteEligibilityTweetFields":  true,
		"withVoice":                               true,
		"withV2Timeline":                          true,
	}, opts, "data", "user", "result", "timeline_v2", "timeline", "instructions")
}

// UserTweetsAndReplies scrapes tweets + replies via UserTweetsAndReplies.
func (c *Client) UserTweetsAndReplies(ctx context.Context, screenName string, opts TimelineOptions) ([]*Tweet, error) {
	userID, err := c.resolveUserID(ctx, screenName)
	if err != nil {
		return nil, err
	}
	return c.scrapeUserTimeline(ctx, "UserTweetsAndReplies", map[string]any{
		"userId":                 userID,
		"count":                  20,
		"includePromotedContent": false,
		"withCommunity":          true,
		"withVoice":              true,
		"withV2Timeline":         true,
	}, opts, "data", "user", "result", "timeline_v2", "timeline", "instructions")
}

func (c *Client) scrapeUserTimeline(
	ctx context.Context,
	endpointName string,
	baseVars map[string]any,
	opts TimelineOptions,
	pathToInstructions ...string,
) ([]*Tweet, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	cursor := opts.Cursor
	out := make([]*Tweet, 0, limit)

	for len(out) < limit {
		vars := copyMap(baseVars)
		if cursor != "" {
			vars["cursor"] = cursor
		}
		var raw map[string]any
		if err := c.GraphQL(ctx, endpointName, vars, &raw); err != nil {
			return nil, err
		}
		insts := walkPathSlice(raw, pathToInstructions...)
		tweets, bottom := ParseTimelineInstructions(insts)
		for _, t := range tweets {
			if len(out) >= limit {
				break
			}
			out = append(out, t)
		}
		if opts.OnPage != nil {
			opts.OnPage(len(out), limit)
		}
		if bottom == "" || len(tweets) == 0 {
			break
		}
		cursor = bottom
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// GetTweet fetches a single tweet by ID via TweetResultByRestId.
func (c *Client) GetTweet(ctx context.Context, tweetID string) (*Tweet, error) {
	vars := map[string]any{
		"tweetId":                tweetID,
		"withCommunity":          false,
		"includePromotedContent": false,
		"withVoice":              false,
	}
	var raw map[string]any
	if err := c.GraphQL(ctx, "TweetResultByRestId", vars, &raw); err != nil {
		return nil, err
	}
	res := walkPathMap(raw, "data", "tweetResult", "result")
	if res == nil {
		return nil, &NotFoundError{Endpoint: "tweet:" + tweetID}
	}
	t := ParseTweet(res)
	if t == nil || t.Tombstone {
		return nil, &NotFoundError{Endpoint: "tweet:" + tweetID}
	}
	return t, nil
}

// ThreadOptions tunes thread reconstruction.
type ThreadOptions struct {
	// AllAuthors includes replies from anyone, not just the original
	// author. Default behavior (false) is "self-thread only".
	AllAuthors bool
}

// GetThread reconstructs a conversation from any tweet in it. Uses
// TweetDetail to walk the conversation, defaults to filtering for the
// thread author (self-thread), sorts chronologically.
func (c *Client) GetThread(ctx context.Context, tweetID string, opts ThreadOptions) (*Thread, error) {
	vars := map[string]any{
		"focalTweetId":                            tweetID,
		"with_rux_injections":                     false,
		"rankingMode":                             "Relevance",
		"includePromotedContent":                  false,
		"withCommunity":                           true,
		"withQuickPromoteEligibilityTweetFields":  true,
		"withBirdwatchNotes":                      true,
		"withVoice":                               true,
	}
	var raw map[string]any
	if err := c.GraphQL(ctx, "TweetDetail", vars, &raw); err != nil {
		return nil, err
	}
	insts := walkPathSlice(raw, "data", "threaded_conversation_with_injections_v2", "instructions")

	all := []*Tweet{}
	for _, inst := range insts {
		im, ok := inst.(map[string]any)
		if !ok {
			continue
		}
		for _, e := range getSlice(im, "entries") {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if t := extractTweetFromEntry(em); t != nil {
				all = append(all, t)
			}
			for _, mi := range walkPathSlice(em, "content", "items") {
				if t := extractTweetFromModuleItem(mi); t != nil {
					all = append(all, t)
				}
			}
		}
	}
	if len(all) == 0 {
		return nil, &NotFoundError{Endpoint: "thread:" + tweetID}
	}

	// Find the focal tweet to identify the author.
	var threadAuthorID string
	for _, t := range all {
		if t.ID == tweetID {
			threadAuthorID = t.Author.ID
			break
		}
	}

	// Filter to same author unless AllAuthors.
	thread := all
	if !opts.AllAuthors && threadAuthorID != "" {
		filtered := thread[:0:0]
		for _, t := range all {
			if t.Author.ID == threadAuthorID {
				filtered = append(filtered, t)
			}
		}
		thread = filtered
	}

	// Sort chronologically. Tweets without a parsable timestamp sort to the
	// front (they're most likely the root).
	sort.SliceStable(thread, func(i, j int) bool {
		return parseISOToUnix(thread[i].CreatedAt) < parseISOToUnix(thread[j].CreatedAt)
	})

	var root *Tweet
	if len(thread) > 0 {
		root = thread[0]
	}
	return &Thread{
		Root:         root,
		Tweets:       thread,
		TotalReplies: len(all) - 1,
	}, nil
}

// resolveUserID looks up a screen name and returns the numeric user ID
// (X's `rest_id`). Cached per Client for the process lifetime so that
// `monitor` and other paginated commands do not pay the UserByScreenName
// roundtrip on every page. The cache key is the lowercased screen name.
//
// Cache is never invalidated — a screen name that changes ID mid-run
// would be a renamed account, which is rare and not worth the complexity.
func (c *Client) resolveUserID(ctx context.Context, screenName string) (string, error) {
	key := strings.ToLower(strings.TrimPrefix(screenName, "@"))
	if cached, ok := c.userIDCache.Load(key); ok {
		return cached.(string), nil
	}

	p, err := c.GetProfile(ctx, key)
	if err != nil {
		return "", err
	}
	if p == nil || p.RestID == "" {
		return "", &NotFoundError{Endpoint: "user:" + screenName}
	}
	c.userIDCache.Store(key, p.RestID)
	return p.RestID, nil
}

// -----------------------------------------------------------------------------
// Internal helpers
// -----------------------------------------------------------------------------

func pickBestVideoURL(variants []any) string {
	type v struct {
		bitrate int
		url     string
	}
	var best v
	for _, vv := range variants {
		vm, ok := vv.(map[string]any)
		if !ok {
			continue
		}
		if getString(vm, "content_type") != "video/mp4" {
			continue
		}
		u := getString(vm, "url")
		if u == "" {
			continue
		}
		if br := getInt(vm, "bitrate"); br >= best.bitrate {
			best = v{bitrate: br, url: u}
		}
	}
	return best.url
}

func parseTwitterDate(raw string) string {
	if raw == "" {
		return ""
	}
	// Twitter format: "Mon Jan 02 15:04:05 -0700 2006"
	t, err := time.Parse(time.RubyDate, raw)
	if err != nil {
		return raw
	}
	return t.UTC().Format(time.RFC3339)
}

func parseISOToUnix(iso string) int64 {
	if iso == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// stripHTMLTags removes HTML tags from a string. Used to clean up the
// `source` field which X serves as `<a href="...">Twitter Web App</a>`.
func stripHTMLTags(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}


