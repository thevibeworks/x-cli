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
	tweetsListLimit          int
	tweetsListIncludeReplies bool
)

var tweetsCmd = &cobra.Command{
	Use:   "tweets",
	Short: "Tweet scraping (list, get)",
}

var tweetsListCmd = &cobra.Command{
	Use:   "list <screen-name>",
	Short: "Scrape a user's tweets (UserTweets)",
	Args:  cobra.ExactArgs(1),
	RunE:  runTweetsList,
}

var tweetsGetCmd = &cobra.Command{
	Use:   "get <tweet-id>",
	Short: "Fetch a single tweet by ID (TweetResultByRestId)",
	Args:  cobra.ExactArgs(1),
	RunE:  runTweetsGet,
}

func init() {
	tweetsListCmd.Flags().IntVarP(&tweetsListLimit, "limit", "n", 50, "max tweets to fetch")
	tweetsListCmd.Flags().BoolVar(&tweetsListIncludeReplies, "replies", false, "include replies (UserTweetsAndReplies)")

	tweetsCmd.AddCommand(tweetsListCmd)
	tweetsCmd.AddCommand(tweetsGetCmd)
	rootCmd.AddCommand(tweetsCmd)
}

func runTweetsList(cmd *cobra.Command, args []string) error {
	screen := strings.TrimPrefix(args[0], "@")

	client, err := newClient(cmd.Context())
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(cmd.Context())
	defer cancel()

	tweets, err := client.UserTweets(ctx, screen, api.TimelineOptions{
		Limit:          tweetsListLimit,
		IncludeReplies: tweetsListIncludeReplies,
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
		return cmdutil.PrintJSON(tweets)
	}
	return renderTweetList(tweets)
}

func runTweetsGet(cmd *cobra.Command, args []string) error {
	tweetID := args[0]

	client, err := newClient(cmd.Context())
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(cmd.Context())
	defer cancel()

	t, err := client.GetTweet(ctx, tweetID)
	if err != nil {
		return err
	}
	if jsonOut {
		return cmdutil.PrintJSON(t)
	}
	return renderTweet(t)
}

func renderTweetList(tweets []*api.Tweet) error {
	if len(tweets) == 0 {
		cmdutil.Warn("no tweets returned")
		return nil
	}
	for _, t := range tweets {
		fmt.Fprintf(os.Stdout, "%-19s  %3sL %3sR %3sV  %s\n",
			t.ID,
			cmdutil.HumanCount(t.Metrics.Likes),
			cmdutil.HumanCount(t.Metrics.Retweets),
			cmdutil.HumanCount(t.Metrics.Views),
			cmdutil.TruncateRunes(cmdutil.SingleLine(t.Text), 100),
		)
	}
	return nil
}

func renderTweet(t *api.Tweet) error {
	tw := cmdutil.NewTabPrinter(os.Stdout)
	tw.Row("id", t.ID)
	tw.Row("author", "@"+t.Author.Username+"  ("+t.Author.Name+")")
	tw.Row("created", t.CreatedAt)
	tw.Row("likes", t.Metrics.Likes)
	tw.Row("retweets", t.Metrics.Retweets)
	tw.Row("replies", t.Metrics.Replies)
	tw.Row("quotes", t.Metrics.Quotes)
	tw.Row("views", t.Metrics.Views)
	if t.Lang != "" {
		tw.Row("lang", t.Lang)
	}
	if t.IsReply && t.InReplyTo != nil {
		tw.Row("reply_to", "@"+t.InReplyTo.Username+" ("+t.InReplyTo.TweetID+")")
	}
	if t.IsRetweet && t.RetweetOf != nil {
		tw.Row("retweet_of", t.RetweetOf.ID)
	}
	if t.Quoted != nil {
		tw.Row("quotes_tweet", t.Quoted.ID)
	}
	if len(t.Media) > 0 {
		tw.Row("media", fmt.Sprintf("%d item(s)", len(t.Media)))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, t.Text)
	return nil
}
