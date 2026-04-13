package cmd

// Placeholder subcommands for v0.1 scope. Each one compiles and surfaces
// "not implemented yet" so the CLI's command tree matches the SKILL.md shape
// from day one. Real implementations land in dedicated files (followers.go,
// tweets.go, etc.) as they're built out.

import (
	"fmt"

	"github.com/spf13/cobra"
)

func notYet(name string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: "(stub — not implemented yet)",
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("%s: not implemented yet", name)
		},
	}
}

func init() {
	tweets := &cobra.Command{Use: "tweets", Short: "Tweet scraping"}
	tweets.AddCommand(notYet("list"))
	tweets.AddCommand(notYet("get"))
	tweets.AddCommand(notYet("replies"))
	rootCmd.AddCommand(tweets)

	search := &cobra.Command{Use: "search", Short: "Search scraping"}
	search.AddCommand(notYet("posts"))
	search.AddCommand(notYet("users"))
	rootCmd.AddCommand(search)

	rootCmd.AddCommand(notYet("followers"))
	rootCmd.AddCommand(notYet("following"))

	thread := &cobra.Command{Use: "thread", Short: "Thread operations"}
	thread.AddCommand(notYet("unroll"))
	rootCmd.AddCommand(thread)

	media := &cobra.Command{Use: "media", Short: "Media download"}
	media.AddCommand(notYet("download"))
	rootCmd.AddCommand(media)

	monitor := &cobra.Command{Use: "monitor", Short: "Live monitoring"}
	monitor.AddCommand(notYet("account"))
	rootCmd.AddCommand(monitor)

	grow := &cobra.Command{
		Use:   "grow",
		Short: "Growth automations (dry-run by default, --apply to mutate)",
	}
	grow.AddCommand(notYet("follow-engagers"))
	grow.AddCommand(notYet("follow-by-keyword"))
	rootCmd.AddCommand(grow)
}
