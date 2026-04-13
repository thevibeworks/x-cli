package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// requestSnapshot captures one server-side request for assertion.
type requestSnapshot struct {
	URL    *url.URL
	Method string
	Header http.Header
	Body   string
}

// newFakeGraphQLServer spins up an httptest.Server that records each
// inbound request and lets the test's handler decide how to reply.
func newFakeGraphQLServer(t *testing.T, handler func(*requestSnapshot) (status int, body string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		snap := &requestSnapshot{
			URL:    r.URL,
			Method: r.Method,
			Header: r.Header.Clone(),
			Body:   string(bs),
		}
		status, body := handler(snap)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// newClientForTest constructs a Client pointing at a test server with a
// given GraphQL endpoint map. Throttle is wide-open so reads never block.
func newClientForTest(serverURL string, gql map[string]GraphQLEndpoint) *Client {
	eps := &EndpointMap{
		Bases:    Bases{GraphQL: serverURL, REST: serverURL, API: serverURL},
		Bearer:   "TESTBEARER",
		Features: map[string]bool{"test_feature": true},
		GraphQL:  gql,
	}
	c := New(Options{
		Endpoints: eps,
		Throttle:  NewThrottle(Defaults{}),
		Session:   Session{Cookies: map[string]string{"auth_token": "x", "ct0": "y"}},
	})
	c.setRetryBackoff(5_000_000) // 5ms
	return c
}

// fixturePagedResponse builds a small UserTweets response with one tweet
// and an optional bottom cursor.
func fixturePagedResponse(tweetID, cursor string) string {
	cursorEntry := ""
	if cursor != "" {
		cursorEntry = fmt.Sprintf(`,
              {
                "entryId": "cursor-bottom-X",
                "content": {"value": %q, "cursorType": "Bottom"}
              }`, cursor)
	}
	return fmt.Sprintf(`{
  "data": {
    "user": {
      "result": {
        "timeline_v2": {
          "timeline": {
            "instructions": [
              {
                "type": "TimelineAddEntries",
                "entries": [
                  {
                    "entryId": "tweet-%s",
                    "content": {
                      "itemContent": {
                        "tweet_results": {
                          "result": {
                            "__typename": "Tweet",
                            "rest_id": %q,
                            "core": {"user_results": {"result": {"rest_id": "u1", "legacy": {"screen_name": "alice", "name": "Alice"}}}},
                            "legacy": {"full_text": "tweet body %s", "favorite_count": 5}
                          }
                        }
                      }
                    }
                  }%s
                ]
              }
            ]
          }
        }
      }
    }
  }
}`, tweetID, tweetID, tweetID, cursorEntry)
}

// fixtureTweet is a minimal but realistic UserTweets response shape with
// one regular tweet, one tombstone, one TweetWithVisibilityResults wrap,
// and a quote tweet. Built by hand from XActions' shape documentation.
const fixtureUserTweetsResponse = `{
  "data": {
    "user": {
      "result": {
        "timeline_v2": {
          "timeline": {
            "instructions": [
              {
                "type": "TimelinePinEntry",
                "entry": {
                  "entryId": "tweet-pinned",
                  "content": {
                    "itemContent": {
                      "tweet_results": {
                        "result": {
                          "__typename": "Tweet",
                          "rest_id": "1000",
                          "core": {
                            "user_results": {
                              "result": {
                                "rest_id": "u1",
                                "is_blue_verified": true,
                                "legacy": {
                                  "screen_name": "alice",
                                  "name": "Alice"
                                }
                              }
                            }
                          },
                          "legacy": {
                            "full_text": "pinned tweet",
                            "favorite_count": 10,
                            "retweet_count": 2,
                            "reply_count": 1,
                            "quote_count": 0,
                            "lang": "en",
                            "created_at": "Mon Jan 02 15:04:05 +0000 2026"
                          },
                          "views": {"count": "999"}
                        }
                      }
                    }
                  }
                }
              },
              {
                "type": "TimelineAddEntries",
                "entries": [
                  {
                    "entryId": "tweet-1234",
                    "content": {
                      "itemContent": {
                        "tweet_results": {
                          "result": {
                            "__typename": "TweetWithVisibilityResults",
                            "tweet": {
                              "rest_id": "1234",
                              "core": {
                                "user_results": {
                                  "result": {
                                    "rest_id": "u1",
                                    "legacy": {"screen_name": "alice", "name": "Alice"}
                                  }
                                }
                              },
                              "legacy": {
                                "full_text": "wrapped tweet with quote",
                                "favorite_count": 50,
                                "retweet_count": 5,
                                "reply_count": 3,
                                "quote_count": 0,
                                "in_reply_to_status_id_str": "9999",
                                "in_reply_to_user_id_str": "u9",
                                "in_reply_to_screen_name": "bob",
                                "extended_entities": {
                                  "media": [
                                    {
                                      "type": "video",
                                      "media_url_https": "https://cdn.x.com/img/poster.jpg",
                                      "video_info": {
                                        "variants": [
                                          {"content_type": "video/mp4", "bitrate": 320000, "url": "https://cdn.x.com/lo.mp4"},
                                          {"content_type": "video/mp4", "bitrate": 832000, "url": "https://cdn.x.com/hi.mp4"},
                                          {"content_type": "application/x-mpegURL", "url": "https://cdn.x.com/playlist.m3u8"}
                                        ]
                                      }
                                    }
                                  ]
                                }
                              },
                              "quoted_status_result": {
                                "result": {
                                  "__typename": "Tweet",
                                  "rest_id": "9001",
                                  "core": {"user_results": {"result": {"rest_id": "u2", "legacy": {"screen_name": "carol"}}}},
                                  "legacy": {"full_text": "quoted body", "favorite_count": 1}
                                }
                              }
                            }
                          }
                        }
                      }
                    }
                  },
                  {
                    "entryId": "tweet-tomb",
                    "content": {
                      "itemContent": {
                        "tweet_results": {
                          "result": {
                            "__typename": "TweetTombstone",
                            "tombstone": {"text": {"text": "This Tweet was deleted"}}
                          }
                        }
                      }
                    }
                  },
                  {
                    "entryId": "cursor-bottom-NEXT",
                    "content": {
                      "value": "PAGE2_CURSOR",
                      "cursorType": "Bottom"
                    }
                  }
                ]
              }
            ]
          }
        }
      }
    }
  }
}`

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParseTimelineInstructionsRichFixture(t *testing.T) {
	root := decode(t, fixtureUserTweetsResponse)
	insts := walkPathSlice(root, "data", "user", "result", "timeline_v2", "timeline", "instructions")
	tweets, cursor := ParseTimelineInstructions(insts)

	if cursor != "PAGE2_CURSOR" {
		t.Errorf("cursor = %q, want PAGE2_CURSOR", cursor)
	}
	// Pinned tweet (1000) + wrapped tweet (1234). Tombstone has no ID so it's
	// dropped from the slice via the ID guard in extractTweetFromEntry.
	if len(tweets) != 2 {
		t.Fatalf("got %d tweets, want 2", len(tweets))
	}

	// First: pinned tweet via TimelinePinEntry.
	if tweets[0].ID != "1000" {
		t.Errorf("tweets[0].ID = %q", tweets[0].ID)
	}
	if !tweets[0].Author.Verified {
		t.Error("pinned tweet author should be verified")
	}
	if tweets[0].Metrics.Views != 999 {
		t.Errorf("pinned tweet views = %d", tweets[0].Metrics.Views)
	}

	// Second: TweetWithVisibilityResults unwrapped, with quote and media.
	tw := tweets[1]
	if tw.ID != "1234" {
		t.Errorf("tweets[1].ID = %q", tw.ID)
	}
	if !tw.IsReply || tw.InReplyTo == nil || tw.InReplyTo.Username != "bob" {
		t.Errorf("reply context wrong: %+v", tw.InReplyTo)
	}
	if tw.Quoted == nil || tw.Quoted.ID != "9001" {
		t.Errorf("quoted = %+v", tw.Quoted)
	}
	if len(tw.Media) != 1 {
		t.Fatalf("media = %d", len(tw.Media))
	}
	if tw.Media[0].VideoURL != "https://cdn.x.com/hi.mp4" {
		t.Errorf("video URL = %q (want highest bitrate hi.mp4)", tw.Media[0].VideoURL)
	}
	if tw.Media[0].Type != "video" {
		t.Errorf("media type = %q", tw.Media[0].Type)
	}
}

func TestParseTweetTombstone(t *testing.T) {
	raw := map[string]any{
		"__typename": "TweetTombstone",
		"tombstone":  map[string]any{"text": map[string]any{"text": "Withheld"}},
	}
	tt := ParseTweet(raw)
	if tt == nil {
		t.Fatal("nil")
	}
	if !tt.Tombstone {
		t.Error("Tombstone flag should be set")
	}
	if tt.Text != "Withheld" {
		t.Errorf("Text = %q", tt.Text)
	}
	if tt.ID != "" {
		t.Errorf("Tombstone ID should be empty, got %q", tt.ID)
	}
}

func TestParseTweetMinimal(t *testing.T) {
	raw := map[string]any{
		"__typename": "Tweet",
		"rest_id":    "42",
		"core": map[string]any{
			"user_results": map[string]any{
				"result": map[string]any{
					"rest_id": "u1",
					"legacy":  map[string]any{"screen_name": "jack", "name": "Jack"},
				},
			},
		},
		"legacy": map[string]any{
			"full_text":      "hello world",
			"favorite_count": float64(7),
		},
	}
	tt := ParseTweet(raw)
	if tt == nil {
		t.Fatal("nil")
	}
	if tt.ID != "42" || tt.Text != "hello world" {
		t.Errorf("got %+v", tt)
	}
	if tt.Metrics.Likes != 7 {
		t.Errorf("likes = %d", tt.Metrics.Likes)
	}
	if tt.Author.Username != "jack" {
		t.Errorf("author = %+v", tt.Author)
	}
}

func TestParseTweetNilSafe(t *testing.T) {
	if got := ParseTweet(nil); got != nil {
		t.Errorf("want nil, got %+v", got)
	}
	if got := ParseTweet("not a map"); got != nil {
		t.Errorf("want nil for non-map, got %+v", got)
	}
}

func TestParseTweetDepthCap(t *testing.T) {
	// Build a quote-of-quote-of-quote chain deeper than maxQuoteDepth.
	build := func(id int, quoted map[string]any) map[string]any {
		m := map[string]any{
			"__typename": "Tweet",
			"rest_id":    fmt.Sprintf("%d", id),
			"core": map[string]any{
				"user_results": map[string]any{
					"result": map[string]any{"rest_id": "u", "legacy": map[string]any{"screen_name": "a"}},
				},
			},
			"legacy": map[string]any{"full_text": fmt.Sprintf("level %d", id)},
		}
		if quoted != nil {
			m["quoted_status_result"] = map[string]any{"result": quoted}
		}
		return m
	}
	deepest := build(maxQuoteDepth+5, nil)
	root := deepest
	for i := maxQuoteDepth + 4; i >= 0; i-- {
		root = build(i, root)
	}
	t1 := ParseTweet(root)
	if t1 == nil {
		t.Fatal("nil root")
	}
	depth := 0
	for cur := t1; cur != nil && depth < 100; cur = cur.Quoted {
		depth++
	}
	if depth > maxQuoteDepth+1 {
		t.Errorf("recursion went %d levels deep, expected ≤ %d", depth, maxQuoteDepth+1)
	}
}

func TestPickBestVideoURL(t *testing.T) {
	variants := []any{
		map[string]any{"content_type": "video/mp4", "bitrate": float64(320000), "url": "lo"},
		map[string]any{"content_type": "video/mp4", "bitrate": float64(832000), "url": "hi"},
		map[string]any{"content_type": "video/mp4", "bitrate": float64(632000), "url": "med"},
		map[string]any{"content_type": "application/x-mpegURL", "url": "playlist"},
	}
	if got := pickBestVideoURL(variants); got != "hi" {
		t.Errorf("got %q, want hi", got)
	}
}

func TestStripHTMLTags(t *testing.T) {
	cases := map[string]string{
		`<a href="https://x.com">Twitter Web App</a>`: "Twitter Web App",
		`plain text`:                                  "plain text",
		`<b>bold</b>`:                                  "bold",
		``:                                             "",
	}
	for in, want := range cases {
		if got := stripHTMLTags(in); got != want {
			t.Errorf("stripHTMLTags(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUserTweetsEndToEnd(t *testing.T) {
	// Two-page response: first call returns one tweet + a cursor, second
	// call returns a second tweet + no cursor.
	calls := 0
	srv := newFakeGraphQLServer(t, func(r *requestSnapshot) (status int, body string) {
		calls++
		switch {
		case strings.Contains(r.URL.Path, "userprofile_qid/UserByScreenName"):
			return 200, `{"data":{"user":{"result":{"rest_id":"u1","legacy":{"screen_name":"alice","name":"Alice"}}}}}`
		case strings.Contains(r.URL.Path, "tweets_qid/UserTweets"):
			if !strings.Contains(r.URL.RawQuery, "u1") {
				t.Errorf("missing userId in vars: %s", r.URL.RawQuery)
			}
			if calls == 2 {
				return 200, fixturePagedResponse("100", "PAGE2_CURSOR")
			}
			return 200, fixturePagedResponse("101", "")
		}
		t.Errorf("unexpected path %q", r.URL.Path)
		return 500, ""
	})
	defer srv.Close()

	c := newClientForTest(srv.URL, map[string]GraphQLEndpoint{
		"UserByScreenName": {QueryID: "userprofile_qid", OperationName: "UserByScreenName", Kind: "read", RPS: 100, Burst: 10},
		"UserTweets":       {QueryID: "tweets_qid", OperationName: "UserTweets", Kind: "read", RPS: 100, Burst: 10},
	})

	tweets, err := c.UserTweets(context.Background(), "alice", TimelineOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(tweets) != 2 {
		t.Errorf("got %d tweets, want 2 (one per page)", len(tweets))
	}
	if tweets[0].ID != "100" || tweets[1].ID != "101" {
		t.Errorf("tweet ids = %v", []string{tweets[0].ID, tweets[1].ID})
	}
}

func TestParseTwitterDate(t *testing.T) {
	got := parseTwitterDate("Mon Jan 02 15:04:05 +0000 2026")
	if got != "2026-01-02T15:04:05Z" {
		t.Errorf("got %q", got)
	}
	if got := parseTwitterDate(""); got != "" {
		t.Errorf("empty input should yield empty, got %q", got)
	}
	if got := parseTwitterDate("not a date"); got != "not a date" {
		t.Errorf("malformed should pass through, got %q", got)
	}
}
