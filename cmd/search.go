package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/x-cli/api"
	"github.com/thevibeworks/x-cli/internal/cmdutil"
)

var (
	searchLimit       int
	searchProduct     string
	searchSince       string
	searchUntil       string
	searchFrom        string
	searchTo          string
	searchLang        string
	searchFilter      string
	searchExclude     string
	searchMinLikes    int
	searchMinRetweets int
)

var searchCmd = &cobra.Command{
	Use:   "search",
	Short: "Search posts and users via SearchTimeline",
}

var searchPostsCmd = &cobra.Command{
	Use:   "posts <query>",
	Short: "Search tweets (--latest by default; --top, --photos, --videos available)",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runSearchPosts,
}

var searchUsersCmd = &cobra.Command{
	Use:   "users <query>",
	Short: "Search user accounts",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runSearchUsers,
}

func init() {
	for _, c := range []*cobra.Command{searchPostsCmd, searchUsersCmd} {
		c.Flags().IntVarP(&searchLimit, "limit", "n", 100, "max items to return")
	}

	searchPostsCmd.Flags().StringVar(&searchProduct, "product", "Latest", "Latest | Top | Photos | Videos")
	searchPostsCmd.Flags().StringVar(&searchSince, "since", "", "YYYY-MM-DD start date")
	searchPostsCmd.Flags().StringVar(&searchUntil, "until", "", "YYYY-MM-DD end date")
	searchPostsCmd.Flags().StringVar(&searchFrom, "from", "", "tweets from this user")
	searchPostsCmd.Flags().StringVar(&searchTo, "to", "", "tweets directed at this user")
	searchPostsCmd.Flags().StringVar(&searchLang, "lang", "", "language code (e.g. en)")
	searchPostsCmd.Flags().StringVar(&searchFilter, "filter", "", "include filter (links | images | videos | media)")
	searchPostsCmd.Flags().StringVar(&searchExclude, "exclude", "", "exclude filter (retweets | replies)")
	searchPostsCmd.Flags().IntVar(&searchMinLikes, "min-likes", 0, "minimum favourite count")
	searchPostsCmd.Flags().IntVar(&searchMinRetweets, "min-retweets", 0, "minimum retweet count")

	searchCmd.AddCommand(searchPostsCmd)
	searchCmd.AddCommand(searchUsersCmd)
	rootCmd.AddCommand(searchCmd)
}

func runSearchPosts(cmd *cobra.Command, args []string) error {
	query := strings.Join(args, " ")
	client, err := newClient(cmd.Context())
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(cmd.Context())
	defer cancel()

	tweets, err := client.SearchPosts(ctx, query, api.SearchOptions{
		Limit:       searchLimit,
		Product:     searchProduct,
		Since:       searchSince,
		Until:       searchUntil,
		From:        searchFrom,
		To:          searchTo,
		Lang:        searchLang,
		Filter:      searchFilter,
		Exclude:     searchExclude,
		MinLikes:    searchMinLikes,
		MinRetweets: searchMinRetweets,
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

func runSearchUsers(cmd *cobra.Command, args []string) error {
	query := strings.Join(args, " ")
	client, err := newClient(cmd.Context())
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(cmd.Context())
	defer cancel()

	users, err := client.SearchUsers(ctx, query, api.SearchOptions{
		Limit: searchLimit,
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

