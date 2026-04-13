package api

import (
	"strings"
	"testing"
)

func TestBuildAdvancedQuery(t *testing.T) {
	cases := []struct {
		name string
		q    string
		opts SearchOptions
		want []string
	}{
		{
			name: "plain",
			q:    "golang",
			opts: SearchOptions{},
			want: []string{"golang"},
		},
		{
			name: "from-since-min",
			q:    "rust",
			opts: SearchOptions{From: "rob", Since: "2026-01-01", MinLikes: 100},
			want: []string{"rust", "from:rob", "since:2026-01-01", "min_faves:100"},
		},
		{
			name: "filter-and-exclude",
			q:    "kubernetes",
			opts: SearchOptions{Filter: "links", Exclude: "retweets"},
			want: []string{"kubernetes", "filter:links", "-filter:retweets"},
		},
		{
			name: "lang-and-min-retweets",
			q:    "ai",
			opts: SearchOptions{Lang: "en", MinRetweets: 50},
			want: []string{"ai", "lang:en", "min_retweets:50"},
		},
		{
			name: "empty-query-with-options",
			q:    "",
			opts: SearchOptions{From: "jack"},
			want: []string{"from:jack"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildAdvancedQuery(tc.q, tc.opts)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("query %q missing %q", got, want)
				}
			}
		})
	}
}

const fixtureSearchUsersResponse = `{
  "data": {
    "search_by_raw_query": {
      "search_timeline": {
        "timeline": {
          "instructions": [
            {
              "type": "TimelineAddEntries",
              "entries": [
                {
                  "entryId": "user-100",
                  "content": {
                    "itemContent": {
                      "user_results": {
                        "result": {
                          "__typename": "User",
                          "rest_id": "100",
                          "legacy": {"screen_name": "first", "name": "First", "followers_count": 5}
                        }
                      }
                    }
                  }
                },
                {
                  "entryId": "cursor-bottom-Y",
                  "content": {"value": "NEXT", "cursorType": "Bottom"}
                }
              ]
            }
          ]
        }
      }
    }
  }
}`

func TestParseSearchUserInstructions(t *testing.T) {
	root := decode(t, fixtureSearchUsersResponse)
	insts := walkPathSlice(root,
		"data", "search_by_raw_query", "search_timeline", "timeline", "instructions")
	users, cursor := parseSearchUserInstructions(insts)
	if cursor != "NEXT" {
		t.Errorf("cursor = %q", cursor)
	}
	if len(users) != 1 || users[0].Username != "first" {
		t.Errorf("users = %+v", users)
	}
}
