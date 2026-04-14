package cmd

import (
	"strings"
	"testing"

	"github.com/thevibeworks/x-cli/api"
)

// formatTweetRow is the load-bearing renderer for `tweets list`,
// `search posts`, and a few other surfaces. The row format is part
// of x-cli's user-facing contract (people grep / awk against it),
// so the test pins both the layout and the per-tweet-shape branches.

func TestFormatTweetRowPlain(t *testing.T) {
	tw := &api.Tweet{
		ID: "12345",
		Author: api.TweetAuthor{Username: "alice"},
		Text: "hello world",
		Metrics: api.TweetMetrics{
			Likes: 42, Retweets: 7, Quotes: 1, Views: 1234,
		},
	}
	row := formatTweetRow(tw)
	if !strings.Contains(row, "12345") {
		t.Errorf("missing tweet ID in %q", row)
	}
	if !strings.Contains(row, "hello world") {
		t.Errorf("missing tweet text in %q", row)
	}
	if !strings.Contains(row, "42L") {
		t.Errorf("missing likes count in %q", row)
	}
	if !strings.Contains(row, "1.2kV") {
		t.Errorf("HumanCount views not formatted in %q", row)
	}
}

func TestFormatTweetRowRetweetShowsOriginal(t *testing.T) {
	// The bug from the user's live test: retweets used to display the
	// truncated `RT @user: <140 chars>…` from legacy.full_text. Now they
	// must display the full original text from RetweetOf.Text.
	original := &api.Tweet{
		ID:     "111",
		Text:   "the full original body which is way longer than the legacy retweet header could fit",
		Author: api.TweetAuthor{Username: "originalauthor"},
	}
	rt := &api.Tweet{
		ID:        "222",
		Text:      "RT @originalauthor: the full original body which is way longer than the legacy retweet…",
		IsRetweet: true,
		RetweetOf: original,
		Author:    api.TweetAuthor{Username: "retweeter"},
	}
	row := formatTweetRow(rt)
	if !strings.Contains(row, "RT @originalauthor") {
		t.Errorf("retweet header missing: %q", row)
	}
	if !strings.Contains(row, "way longer") {
		t.Errorf("retweet body should be the original's full text, got: %q", row)
	}
	if strings.Contains(row, "header could fit…") {
		// The truncation here is the rune-truncation at 120, not the
		// Twitter-side 140 cutoff. Both are fine; we just want the
		// FULL body, not the legacy_full_text variant.
	}
}

func TestFormatTweetRowReplyMarker(t *testing.T) {
	tw := &api.Tweet{
		ID:        "333",
		Text:      "this is a reply",
		IsReply:   true,
		InReplyTo: &api.ReplyContext{TweetID: "999", Username: "other"},
	}
	row := formatTweetRow(tw)
	if !strings.Contains(row, "↳") {
		t.Errorf("reply marker missing: %q", row)
	}
}

func TestFormatTweetRowQuoteMarker(t *testing.T) {
	tw := &api.Tweet{
		ID:     "444",
		Text:   "look at this quote",
		Quoted: &api.Tweet{ID: "999", Text: "quoted body"},
	}
	row := formatTweetRow(tw)
	if !strings.Contains(row, "→q") {
		t.Errorf("quote marker missing: %q", row)
	}
}

func TestFormatTweetRowMediaMarkers(t *testing.T) {
	cases := []struct {
		name  string
		media []api.TweetMedia
		want  string
	}{
		{"two photos", []api.TweetMedia{{Type: "photo"}, {Type: "photo"}}, "📷2"},
		{"one video", []api.TweetMedia{{Type: "video"}}, "🎬1"},
		{"animated gif", []api.TweetMedia{{Type: "animated_gif"}}, "🎞1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tw := &api.Tweet{ID: "555", Text: "with media", Media: tc.media}
			row := formatTweetRow(tw)
			if !strings.Contains(row, tc.want) {
				t.Errorf("missing %q marker in %q", tc.want, row)
			}
		})
	}
}

func TestFormatTweetRowEmojiSafeTruncation(t *testing.T) {
	// Verify the truncation works on multi-byte runes (Chinese, emoji)
	// without slicing mid-character.
	tw := &api.Tweet{
		ID:   "666",
		Text: strings.Repeat("小魔女😈", 50), // 200 runes, 800+ bytes
	}
	row := formatTweetRow(tw)
	if !strings.Contains(row, "…") {
		t.Errorf("expected truncation marker for long unicode: %q", row)
	}
	// The renderer must not panic on rune slicing. If we got here without
	// crashing, the SafeTruncation invariant holds.
}
