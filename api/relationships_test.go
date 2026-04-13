package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const fixtureFollowersResponse = `{
  "data": {
    "user": {
      "result": {
        "timeline": {
          "timeline": {
            "instructions": [
              {
                "type": "TimelineAddEntries",
                "entries": [
                  {
                    "entryId": "user-1",
                    "content": {
                      "itemContent": {
                        "user_results": {
                          "result": {
                            "__typename": "User",
                            "rest_id": "u1",
                            "is_blue_verified": true,
                            "legacy": {
                              "screen_name": "alice",
                              "name": "Alice",
                              "description": "First user",
                              "followers_count": 1000,
                              "friends_count": 500,
                              "statuses_count": 42,
                              "profile_image_url_https": "https://x.com/alice_normal.jpg"
                            }
                          }
                        }
                      }
                    }
                  },
                  {
                    "entryId": "user-2",
                    "content": {
                      "itemContent": {
                        "user_results": {
                          "result": {
                            "__typename": "User",
                            "rest_id": "u2",
                            "legacy": {
                              "screen_name": "bob",
                              "name": "Bob",
                              "followers_count": 50,
                              "friends_count": 200,
                              "protected": true
                            }
                          }
                        }
                      }
                    }
                  },
                  {
                    "entryId": "user-3",
                    "content": {
                      "itemContent": {
                        "user_results": {
                          "result": {"__typename": "UserUnavailable"}
                        }
                      }
                    }
                  },
                  {
                    "entryId": "cursor-bottom-XYZ",
                    "content": {"value": "NEXT_PAGE", "cursorType": "Bottom"}
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

func TestParseUserListRichFixture(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal([]byte(fixtureFollowersResponse), &root); err != nil {
		t.Fatal(err)
	}
	insts := walkPathSlice(root, "data", "user", "result", "timeline", "timeline", "instructions")
	users, cursor := ParseUserList(insts)

	if cursor != "NEXT_PAGE" {
		t.Errorf("cursor = %q", cursor)
	}
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2 (UserUnavailable should be dropped)", len(users))
	}
	if users[0].Username != "alice" || users[0].Followers != 1000 || !users[0].Verified {
		t.Errorf("alice = %+v", users[0])
	}
	// Avatar should have been bumped from _normal to _400x400.
	if users[0].Avatar != "https://x.com/alice_400x400.jpg" {
		t.Errorf("avatar not bumped: %q", users[0].Avatar)
	}
	if users[1].Username != "bob" || !users[1].Protected {
		t.Errorf("bob = %+v", users[1])
	}
}

func TestParseUserSummaryUnavailable(t *testing.T) {
	raw := map[string]any{"__typename": "UserUnavailable"}
	if got := ParseUserSummary(raw); got != nil {
		t.Errorf("want nil, got %+v", got)
	}
}

func TestParseUserSummaryNilSafe(t *testing.T) {
	if got := ParseUserSummary(nil); got != nil {
		t.Errorf("want nil, got %+v", got)
	}
}

func TestFollowersEndToEnd(t *testing.T) {
	calls := 0
	srv := newFakeGraphQLServer(t, func(r *requestSnapshot) (status int, body string) {
		calls++
		if strings.Contains(r.URL.Path, "user_qid/UserByScreenName") {
			return 200, `{"data":{"user":{"result":{"rest_id":"u1","legacy":{"screen_name":"alice","name":"Alice"}}}}}`
		}
		if strings.Contains(r.URL.Path, "fol_qid/Followers") {
			// First page: two users + cursor. Second page: one duplicate
			// + one new user, no cursor. Validates dedup + multi-page.
			if calls == 2 {
				return 200, fixtureFollowersPage([]string{"alice", "bob"}, "PAGE2")
			}
			return 200, fixtureFollowersPage([]string{"bob", "carol"}, "")
		}
		return 500, ""
	})
	defer srv.Close()

	c := newClientForTest(srv.URL, map[string]GraphQLEndpoint{
		"UserByScreenName": {QueryID: "user_qid", OperationName: "UserByScreenName", Kind: "read", RPS: 100, Burst: 10},
		"Followers":        {QueryID: "fol_qid", OperationName: "Followers", Kind: "read", RPS: 100, Burst: 10},
	})

	users, err := c.Followers(context.Background(), "alice", PageOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 3 {
		t.Errorf("got %d users, want 3 (dedup of bob)", len(users))
	}
	usernames := []string{}
	for _, u := range users {
		usernames = append(usernames, u.Username)
	}
	wantSet := map[string]bool{"alice": true, "bob": true, "carol": true}
	for _, u := range usernames {
		if !wantSet[u] {
			t.Errorf("unexpected user %q", u)
		}
		delete(wantSet, u)
	}
	if len(wantSet) > 0 {
		t.Errorf("missing users: %v", wantSet)
	}
}

func fixtureFollowersPage(usernames []string, cursor string) string {
	var entries strings.Builder
	for i, u := range usernames {
		if i > 0 {
			entries.WriteString(",")
		}
		entries.WriteString(fmt.Sprintf(`{
			"entryId": "user-%s",
			"content": {
				"itemContent": {
					"user_results": {
						"result": {
							"__typename": "User",
							"rest_id": "id-%s",
							"legacy": {"screen_name": %q, "name": %q, "followers_count": 42}
						}
					}
				}
			}
		}`, u, u, u, u))
	}
	if cursor != "" {
		entries.WriteString(fmt.Sprintf(`,{
			"entryId": "cursor-bottom-X",
			"content": {"value": %q, "cursorType": "Bottom"}
		}`, cursor))
	}
	return fmt.Sprintf(`{
		"data": {
			"user": {
				"result": {
					"timeline": {
						"timeline": {
							"instructions": [
								{"type": "TimelineAddEntries", "entries": [%s]}
							]
						}
					}
				}
			}
		}
	}`, entries.String())
}
