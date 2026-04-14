package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thevibeworks/x-cli/api"
	"github.com/thevibeworks/x-cli/internal/cmdutil"
)

var (
	relLimit int
)

var followersCmd = &cobra.Command{
	Use:   "followers <screen-name>",
	Short: "Scrape a user's followers (paginated, deduped)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRelationshipScrape(cmd, args, relationshipFollowers)
	},
}

var followingCmd = &cobra.Command{
	Use:   "following <screen-name>",
	Short: "Scrape who a user is following (paginated, deduped)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRelationshipScrape(cmd, args, relationshipFollowing)
	},
}

var likesCmd = &cobra.Command{
	Use:   "likes <tweet-id|url>",
	Short: "Scrape users who liked a tweet (Favoriters)",
	Args:  cobra.ExactArgs(1),
	RunE:  runScrapeLikers,
}

var retweetersCmd = &cobra.Command{
	Use:   "retweeters <tweet-id|url>",
	Short: "Scrape users who retweeted a tweet",
	Args:  cobra.ExactArgs(1),
	RunE:  runScrapeRetweeters,
}

var nonfollowersCmd = &cobra.Command{
	Use:   "nonfollowers <screen-name>",
	Short: "Find accounts the user follows that don't follow back",
	Long: `Scrapes both the followers and following lists, then takes the
set difference: accounts in `+"`following`"+` that aren't in `+"`followers`"+`.
This is XActions' most-asked feature ("who doesn't follow me back").

Two GraphQL roundtrips per page on each list, so a fresh run on a
~10k account takes a minute or two. The result is sorted by
follower count desc — drop the high-value mutuals first if you
plan to unfollow.`,
	Args: cobra.ExactArgs(1),
	RunE: runScrapeNonFollowers,
}

type relationshipKind int

const (
	relationshipFollowers relationshipKind = iota
	relationshipFollowing
)

func init() {
	for _, c := range []*cobra.Command{followersCmd, followingCmd, likesCmd, retweetersCmd, nonfollowersCmd} {
		c.Flags().IntVarP(&relLimit, "limit", "n", 200, "max users to fetch")
		rootCmd.AddCommand(c)
	}
}

func runScrapeLikers(cmd *cobra.Command, args []string) error {
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
	users, err := client.Likers(ctx, tweetID, api.PageOptions{
		Limit: relLimit,
		OnPage: func(fetched, limit int) {
			if !jsonOut && verbose {
				cmdutil.Info("fetched %d/%d", fetched, limit)
			}
		},
	})
	if err != nil {
		return err
	}
	if jsonOut {
		return cmdutil.PrintJSON(users)
	}
	return renderUserList(users)
}

func runScrapeRetweeters(cmd *cobra.Command, args []string) error {
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
	users, err := client.Retweeters(ctx, tweetID, api.PageOptions{
		Limit: relLimit,
		OnPage: func(fetched, limit int) {
			if !jsonOut && verbose {
				cmdutil.Info("fetched %d/%d", fetched, limit)
			}
		},
	})
	if err != nil {
		return err
	}
	if jsonOut {
		return cmdutil.PrintJSON(users)
	}
	return renderUserList(users)
}

// runScrapeNonFollowers scrapes both the user's followers and following
// lists, then returns following \ followers (set difference).
//
// Implementation note: we read the FULL following list (capped by
// --limit) and the FULL followers list (also capped), then compute
// the set difference in memory keyed by user ID. For users with
// hundreds of thousands of followers, --limit is essentially
// mandatory or both pulls take forever.
func runScrapeNonFollowers(cmd *cobra.Command, args []string) error {
	screen := strings.TrimPrefix(args[0], "@")
	client, err := newClient(cmd.Context())
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(cmd.Context())
	defer cancel()

	cmdutil.Info("scraping %s's following list (cap: %d)...", screen, relLimit)
	following, err := client.Following(ctx, screen, api.PageOptions{Limit: relLimit})
	if err != nil {
		return fmt.Errorf("following: %w", err)
	}

	cmdutil.Info("scraping %s's followers list (cap: %d)...", screen, relLimit)
	followers, err := client.Followers(ctx, screen, api.PageOptions{Limit: relLimit})
	if err != nil {
		return fmt.Errorf("followers: %w", err)
	}

	followerSet := make(map[string]struct{}, len(followers))
	for _, u := range followers {
		followerSet[u.ID] = struct{}{}
	}

	nonback := make([]*api.UserSummary, 0, len(following))
	for _, u := range following {
		if _, ok := followerSet[u.ID]; !ok {
			nonback = append(nonback, u)
		}
	}

	cmdutil.Success("%d / %d accounts you follow do not follow back",
		len(nonback), len(following))

	if jsonOut {
		return cmdutil.PrintJSON(nonback)
	}
	return renderUserList(nonback)
}

func runRelationshipScrape(cmd *cobra.Command, args []string, kind relationshipKind) error {
	screen := strings.TrimPrefix(args[0], "@")
	client, err := newClient(cmd.Context())
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(cmd.Context())
	defer cancel()

	opts := api.PageOptions{
		Limit: relLimit,
		OnPage: func(fetched, limit int) {
			if !jsonOut && verbose {
				cmdutil.Info("fetched %d/%d", fetched, limit)
			}
		},
	}

	var (
		users []*api.UserSummary
	)
	switch kind {
	case relationshipFollowers:
		// The Followers GraphQL endpoint requires the
		// `x-client-transaction-id` header that only x.com's own JS
		// knows how to compute. Direct GraphQL calls 404 on it. Use
		// the DOM-scraping fallback (XActions' Puppeteer approach):
		// navigate to /<user>/followers, let the SPA do the work,
		// read the rendered [data-testid=UserCell] rows.
		users, err = client.FollowersDOM(ctx, screen, opts)
	case relationshipFollowing:
		// Following works via the direct GraphQL path today — it
		// doesn't enforce x-client-transaction-id. Use the faster
		// fetch() path.
		users, err = client.Following(ctx, screen, opts)
	}
	if err != nil {
		return err
	}

	if jsonOut {
		return cmdutil.PrintJSON(users)
	}
	return renderUserList(users)
}

func renderUserList(users []*api.UserSummary) error {
	if len(users) == 0 {
		cmdutil.Warn("no users returned")
		return nil
	}
	for _, u := range users {
		verified := " "
		if u.Verified {
			verified = "✓"
		}
		fmt.Fprintf(os.Stdout, "%s  @%-20s  %5s followers  %s\n",
			verified,
			u.Username,
			cmdutil.HumanCount(u.Followers),
			cmdutil.TruncateRunes(cmdutil.SingleLine(u.Name), 32),
		)
	}
	return nil
}
