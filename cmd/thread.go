package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/x-cli/api"
	"github.com/thevibeworks/x-cli/internal/cmdutil"
)

var threadAllAuthors bool

var threadCmd = &cobra.Command{
	Use:   "thread",
	Short: "Thread reconstruction",
}

var threadUnrollCmd = &cobra.Command{
	Use:   "unroll <tweet-id>",
	Short: "Reconstruct a thread from any tweet in it (TweetDetail)",
	Args:  cobra.ExactArgs(1),
	RunE:  runThreadUnroll,
}

func init() {
	threadUnrollCmd.Flags().BoolVar(&threadAllAuthors, "all-authors", false,
		"include replies from anyone (default: same author only)")
	threadCmd.AddCommand(threadUnrollCmd)
	rootCmd.AddCommand(threadCmd)
}

func runThreadUnroll(cmd *cobra.Command, args []string) error {
	tweetID := args[0]
	client, err := newClient(cmd.Context())
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(cmd.Context())
	defer cancel()

	thread, err := client.GetThread(ctx, tweetID, api.ThreadOptions{AllAuthors: threadAllAuthors})
	if err != nil {
		return err
	}

	if jsonOut {
		return cmdutil.PrintJSON(thread)
	}

	if thread.Root != nil {
		fmt.Fprintf(os.Stdout, "thread by @%s — %d tweets, %d total replies\n\n",
			thread.Root.Author.Username, len(thread.Tweets), thread.TotalReplies)
	}
	for i, t := range thread.Tweets {
		fmt.Fprintf(os.Stdout, "[%d/%d] %s  (%s)\n", i+1, len(thread.Tweets), t.ID, cmdutil.RelTime(t.CreatedAt))
		fmt.Fprintln(os.Stdout, t.Text)
		fmt.Fprintln(os.Stdout)
	}
	return nil
}
