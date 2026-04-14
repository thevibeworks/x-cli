package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/x-cli/api"
	"github.com/thevibeworks/x-cli/internal/cmdutil"
)

// engage groups the lightweight engagement mutations: like, retweet,
// bookmark, and their inverses. These are throttled but not gated
// behind --apply (unlike `grow`) because they target a single tweet
// each — no risk of bulk-following spam, no need for dry-run.
//
// Each subcommand calls the corresponding GraphQL mutation through
// Client.graphqlMutation, which inspects the response envelope and
// treats idempotent failures ("you have already favorited") as
// success. Re-running is safe.
//
// For BULK engagement (follow likers / follow keyword authors), see
// `x grow` which has --apply, --max, --min-followers, and per-mutation
// pacing through Throttle.AwaitMutation.

var engageCmd = &cobra.Command{
	Use:   "engage",
	Short: "Engagement actions on a tweet (like / unlike / bookmark / unbookmark)",
}

var engageLikeCmd = &cobra.Command{
	Use:   "like <tweet-id|url>",
	Short: "Like (favorite) a tweet",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEngageOp(cmd, args[0], "like", (*api.Client).LikeTweet)
	},
}

var engageUnlikeCmd = &cobra.Command{
	Use:   "unlike <tweet-id|url>",
	Short: "Remove a like (unfavorite)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEngageOp(cmd, args[0], "unlike", (*api.Client).UnlikeTweet)
	},
}

var engageBookmarkCmd = &cobra.Command{
	Use:   "bookmark <tweet-id|url>",
	Short: "Bookmark a tweet",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEngageOp(cmd, args[0], "bookmark", (*api.Client).BookmarkTweet)
	},
}

var engageUnbookmarkCmd = &cobra.Command{
	Use:   "unbookmark <tweet-id|url>",
	Short: "Remove a bookmark",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEngageOp(cmd, args[0], "unbookmark", (*api.Client).UnbookmarkTweet)
	},
}

func init() {
	engageCmd.AddCommand(engageLikeCmd)
	engageCmd.AddCommand(engageUnlikeCmd)
	engageCmd.AddCommand(engageBookmarkCmd)
	engageCmd.AddCommand(engageUnbookmarkCmd)
	rootCmd.AddCommand(engageCmd)
}

// runEngageOp resolves the tweet id from arg, builds a client, and
// runs the given method-value op. Method values let us write
//
//	(*api.Client).LikeTweet
//
// once per subcommand instead of duplicating the boilerplate four times.
func runEngageOp(cmd *cobra.Command, arg, label string, op func(*api.Client, context.Context, string) error) error {
	tweetID := extractTweetID(arg)
	if tweetID == "" {
		return fmt.Errorf("could not extract tweet ID from %q", arg)
	}
	client, err := newClient(cmd.Context())
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(cmd.Context())
	defer cancel()

	if err := op(client, ctx, tweetID); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	cmdutil.Success("%s ok: %s", label, tweetID)
	return nil
}
