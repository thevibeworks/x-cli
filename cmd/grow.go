package cmd

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/x-cli/api"
	"github.com/thevibeworks/x-cli/internal/cmdutil"
)

// Grow commands wrap mutations behind a dry-run-by-default safety
// barrier. Three layers of protection:
//
//   1. Dry-run is the default. Caller must pass --apply to mutate.
//   2. The api.Throttle enforces min_gap, jitter, daily cap, autopause.
//   3. We refuse to apply mutations from a cloud ASN (per `x doctor`)
//      unless --i-know-its-a-cloud-ip is also set.

var (
	growMax            int
	growMinFollowers   int
	growApply          bool
	growAllowCloud     bool
)

var growCmd = &cobra.Command{
	Use:   "grow",
	Short: "Growth automations (dry-run by default; --apply to mutate)",
	Long: `grow runs throttled, capped follow operations against an authenticated
session. Every mutation goes through:

  - per-mutation min/max gap with jitter (default 8-22s)
  - global daily cap (default 200)
  - autopause on consecutive errors

Dry-run is the default. Pass --apply to mutate. We refuse to mutate from
a cloud ASN unless --i-know-its-a-cloud-ip is also set.`,
}

var growFollowEngagersCmd = &cobra.Command{
	Use:   "follow-engagers <tweet-id|tweet-url>",
	Short: "Follow likers + retweeters of a tweet",
	Args:  cobra.ExactArgs(1),
	RunE:  runGrowFollowEngagers,
}

var growFollowByKeywordCmd = &cobra.Command{
	Use:   "follow-by-keyword <query>",
	Short: "Follow authors of recent tweets matching a query",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runGrowFollowByKeyword,
}

func init() {
	for _, c := range []*cobra.Command{growFollowEngagersCmd, growFollowByKeywordCmd} {
		c.Flags().IntVarP(&growMax, "max", "n", 25, "maximum follows to perform this run")
		c.Flags().IntVar(&growMinFollowers, "min-followers", 0, "skip targets with fewer followers than N")
		c.Flags().BoolVar(&growApply, "apply", false, "actually mutate (default: dry-run)")
		c.Flags().BoolVar(&growAllowCloud, "i-know-its-a-cloud-ip", false,
			"acknowledge that running mutations from a cloud ASN is high-risk")
	}
	growCmd.AddCommand(growFollowEngagersCmd)
	growCmd.AddCommand(growFollowByKeywordCmd)
	rootCmd.AddCommand(growCmd)
}

// -----------------------------------------------------------------------------
// follow-engagers
// -----------------------------------------------------------------------------

func runGrowFollowEngagers(cmd *cobra.Command, args []string) error {
	tweetID := extractTweetID(args[0])
	if tweetID == "" {
		return fmt.Errorf("could not extract tweet ID from %q", args[0])
	}

	client, err := newClient(cmd.Context())
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(cmd.Context())
	defer cancel()

	cmdutil.Info("collecting engagers of %s ...", tweetID)
	candidatePool := growMax * 3
	if candidatePool < 60 {
		candidatePool = 60
	}
	likers, err := client.Likers(ctx, tweetID, api.PageOptions{Limit: candidatePool})
	if err != nil {
		return fmt.Errorf("likers: %w", err)
	}
	retweeters, err := client.Retweeters(ctx, tweetID, api.PageOptions{Limit: candidatePool})
	if err != nil {
		return fmt.Errorf("retweeters: %w", err)
	}

	candidates := mergeUserCandidates(likers, retweeters)
	candidates = filterUsersByFollowers(candidates, growMinFollowers)
	if len(candidates) > growMax {
		candidates = candidates[:growMax]
	}

	return executeFollowBatch(ctx, client, candidates, "follow-engagers")
}

// -----------------------------------------------------------------------------
// follow-by-keyword
// -----------------------------------------------------------------------------

func runGrowFollowByKeyword(cmd *cobra.Command, args []string) error {
	query := strings.Join(args, " ")
	client, err := newClient(cmd.Context())
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(cmd.Context())
	defer cancel()

	cmdutil.Info("searching tweets for %q ...", query)
	tweets, err := client.SearchPosts(ctx, query, api.SearchOptions{
		Limit:   growMax * 5,
		Product: "Latest",
	})
	if err != nil {
		return err
	}

	// Distinct authors, in order of first appearance.
	seen := map[string]struct{}{}
	candidates := make([]*api.UserSummary, 0, len(tweets))
	for _, t := range tweets {
		if t.Author.ID == "" || t.Author.Username == "" {
			continue
		}
		if _, dup := seen[t.Author.ID]; dup {
			continue
		}
		seen[t.Author.ID] = struct{}{}
		candidates = append(candidates, &api.UserSummary{
			ID:       t.Author.ID,
			Username: t.Author.Username,
			Name:     t.Author.Name,
			Verified: t.Author.Verified,
		})
	}
	candidates = filterUsersByFollowers(candidates, growMinFollowers)
	if len(candidates) > growMax {
		candidates = candidates[:growMax]
	}

	return executeFollowBatch(ctx, client, candidates, "follow-by-keyword")
}

// -----------------------------------------------------------------------------
// Shared mutation runner
// -----------------------------------------------------------------------------

func executeFollowBatch(ctx context.Context, client *api.Client, targets []*api.UserSummary, label string) error {
	if len(targets) == 0 {
		cmdutil.Warn("no candidates after filtering")
		return nil
	}

	cmdutil.Info("%s: %d candidate(s) selected", label, len(targets))
	for _, u := range targets {
		fmt.Fprintf(os.Stdout, "  → @%-20s  %5s followers  %s\n",
			u.Username, cmdutil.HumanCount(u.Followers), u.Name)
	}

	if !growApply {
		cmdutil.Warn("dry-run: pass --apply to actually follow these accounts")
		return nil
	}

	if !growAllowCloud && EgressIsCloud(ctx) {
		return errors.New(
			"refusing to mutate: egress IP looks like a cloud ASN. " +
				"Run from a residential connection, or pass --i-know-its-a-cloud-ip " +
				"to override (this is exactly the asymmetry X's anti-abuse models flag).")
	}

	cmdutil.Warn("applying — this respects the throttle (min/max gap + daily cap)")

	var (
		ok      int
		skipped int
		failed  int
	)
	for i, u := range targets {
		err := client.FollowUser(ctx, u.ID)
		var (
			notFound *api.NotFoundError
			rateLim  *api.RateLimitError
			budget   *api.BudgetExhaustedError
		)
		switch {
		case err == nil:
			ok++
			cmdutil.Success("[%d/%d] followed @%s", i+1, len(targets), u.Username)
		case errors.As(err, &notFound):
			skipped++
			cmdutil.Warn("[%d/%d] @%s not found, skipped", i+1, len(targets), u.Username)
		case errors.As(err, &rateLim):
			cmdutil.Fail("[%d/%d] rate limited; stopping batch: %v", i+1, len(targets), err)
			return err
		case errors.As(err, &budget):
			cmdutil.Warn("[%d/%d] daily mutation budget exhausted; stopping", i+1, len(targets))
			return nil
		default:
			failed++
			cmdutil.Fail("[%d/%d] @%s: %v", i+1, len(targets), u.Username, err)
		}
	}

	cmdutil.Success("done: %d followed, %d skipped, %d failed", ok, skipped, failed)
	return nil
}


func filterUsersByFollowers(in []*api.UserSummary, minFollowers int) []*api.UserSummary {
	if minFollowers <= 0 {
		return in
	}
	out := in[:0:0]
	for _, u := range in {
		if u.Followers >= minFollowers {
			out = append(out, u)
		}
	}
	return out
}

// mergeUserCandidates dedups, log-buckets by follower count, and shuffles
// within each bucket. Pure follower-desc sort would always engage the
// same whales first across runs — a behavioral signature anti-abuse
// systems trivially fingerprint. Bucketing preserves "high-quality first"
// while breaking the deterministic order between candidates of similar
// reach.
func mergeUserCandidates(lists ...[]*api.UserSummary) []*api.UserSummary {
	seen := map[string]struct{}{}
	out := []*api.UserSummary{}
	for _, list := range lists {
		for _, u := range list {
			if u == nil || u.ID == "" {
				continue
			}
			if _, dup := seen[u.ID]; dup {
				continue
			}
			seen[u.ID] = struct{}{}
			out = append(out, u)
		}
	}
	// Sort into descending log-buckets by follower count, then shuffle
	// within each bucket so the outer order remains "biggest accounts
	// first" but the within-bucket order is fresh per run.
	bucket := func(n int) int {
		// 0, 1-9, 10-99, 100-999, 1k-9999, ..., 1M+, 10M+
		if n <= 0 {
			return 0
		}
		b := 1
		for n >= 10 {
			n /= 10
			b++
		}
		return b
	}
	sort.SliceStable(out, func(i, j int) bool {
		return bucket(out[i].Followers) > bucket(out[j].Followers)
	})
	// Shuffle within each contiguous bucket.
	start := 0
	for i := 1; i <= len(out); i++ {
		if i == len(out) || bucket(out[i].Followers) != bucket(out[start].Followers) {
			run := out[start:i]
			rand.Shuffle(len(run), func(a, b int) { run[a], run[b] = run[b], run[a] })
			start = i
		}
	}
	return out
}

