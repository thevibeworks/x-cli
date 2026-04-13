package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validEndpointsYAML = `
bases:
  graphql: https://x.com/i/api/graphql
  rest: https://x.com/i/api
  api: https://api.x.com
bearer: AAA
features:
  foo: true
  bar: false
graphql:
  UserByScreenName:
    queryId: qid1
    operationName: UserByScreenName
    kind: read
    rps: 0.5
    burst: 2
  Followers:
    queryId: qid2
    operationName: Followers
    kind: read
    rps: 0.2
    burst: 1
rest:
  friendshipsCreate:
    path: /1.1/friendships/create.json
    method: POST
    kind: mutation
    min_gap: 8s
    max_gap: 22s
    daily_cap: 200
  verifyCredentials:
    path: /1.1/account/verify_credentials.json
    method: GET
    kind: read
`

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadEndpointsValid(t *testing.T) {
	p := writeTemp(t, "good.yaml", validEndpointsYAML)
	m, err := LoadEndpoints(p)
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if m.Bearer != "AAA" {
		t.Errorf("Bearer = %q", m.Bearer)
	}
	if m.Bases.GraphQL != "https://x.com/i/api/graphql" {
		t.Errorf("GraphQL base = %q", m.Bases.GraphQL)
	}
	if len(m.GraphQL) != 2 {
		t.Errorf("GraphQL count = %d", len(m.GraphQL))
	}
	if ep, ok := m.GraphQL["UserByScreenName"]; !ok {
		t.Error("UserByScreenName missing")
	} else if ep.QueryID != "qid1" || ep.RPS != 0.5 {
		t.Errorf("UserByScreenName = %+v", ep)
	}
	if ep, ok := m.REST["friendshipsCreate"]; !ok {
		t.Error("friendshipsCreate missing")
	} else {
		if ep.MinGap != 8*time.Second {
			t.Errorf("MinGap = %v", ep.MinGap)
		}
		if ep.MaxGap != 22*time.Second {
			t.Errorf("MaxGap = %v", ep.MaxGap)
		}
		if ep.DailyCap != 200 {
			t.Errorf("DailyCap = %d", ep.DailyCap)
		}
	}
	if !m.Features["foo"] {
		t.Error("feature foo should be true")
	}
	if m.Features["bar"] {
		t.Error("feature bar should be false")
	}
}

func TestLoadEndpointsMissingFile(t *testing.T) {
	_, err := LoadEndpoints(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("expected error on missing file")
	}
}

func TestLoadEndpointsInvalidYAML(t *testing.T) {
	p := writeTemp(t, "bad.yaml", "not: yaml: [unterminated")
	_, err := LoadEndpoints(p)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention parse, got %v", err)
	}
}

func TestLoadEndpointsMissingRequired(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no-bearer", "bases:\n  graphql: https://x.com\n"},
		{"no-graphql-base", "bearer: AAA\n"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeTemp(t, "missing.yaml", tc.body)
			_, err := LoadEndpoints(p)
			if err == nil {
				t.Fatal("expected required-field error")
			}
		})
	}
}

// TestLoadShippedEndpoints is a smoke test against the endpoints.yaml shipped
// with the binary. If this breaks, the shipped file is invalid.
func TestLoadShippedEndpoints(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	m, err := LoadEndpoints(filepath.Join(root, "endpoints.yaml"))
	if err != nil {
		t.Fatalf("shipped endpoints.yaml failed to load: %v", err)
	}
	required := []string{
		"UserByScreenName",
		"UserByRestId",
		"UserTweets",
		"TweetDetail",
		"SearchTimeline",
		"Followers",
		"Following",
	}
	for _, name := range required {
		if _, ok := m.GraphQL[name]; !ok {
			t.Errorf("shipped endpoints.yaml missing %s", name)
		}
	}
	if _, ok := m.REST["friendshipsCreate"]; !ok {
		t.Error("shipped endpoints.yaml missing friendshipsCreate")
	}
	if _, ok := m.REST["friendshipsDestroy"]; !ok {
		t.Error("shipped endpoints.yaml missing friendshipsDestroy")
	}
	if m.Bearer == "" {
		t.Error("shipped endpoints.yaml has empty bearer")
	}
	// verifyCredentials.json was removed by X — the shipped YAML
	// must NOT carry it any more (the auth liveness check now goes
	// through UserByRestId).
	if _, ok := m.REST["verifyCredentials"]; ok {
		t.Error("shipped endpoints.yaml should no longer carry the dead verifyCredentials REST entry")
	}
}
