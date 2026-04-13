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

type relationshipKind int

const (
	relationshipFollowers relationshipKind = iota
	relationshipFollowing
)

func init() {
	for _, c := range []*cobra.Command{followersCmd, followingCmd} {
		c.Flags().IntVarP(&relLimit, "limit", "n", 200, "max users to fetch")
		rootCmd.AddCommand(c)
	}
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
		users, err = client.Followers(ctx, screen, opts)
	case relationshipFollowing:
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
