package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/thevibeworks/x-cli/api"
	"github.com/spf13/cobra"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Profile scraping commands",
}

var profileGetCmd = &cobra.Command{
	Use:   "get <screen-name>",
	Short: "Fetch a user's profile (UserByScreenName)",
	Args:  cobra.ExactArgs(1),
	RunE:  runProfileGet,
}

func init() {
	profileCmd.AddCommand(profileGetCmd)
	rootCmd.AddCommand(profileCmd)
}

func runProfileGet(cmd *cobra.Command, args []string) error {
	screen := strings.TrimPrefix(args[0], "@")

	client, err := newClient(cmd.Context())
	if err != nil {
		return err
	}

	ctx, cancel := withTimeout(cmd.Context())
	defer cancel()

	p, err := client.GetProfile(ctx, screen)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("user @%s not found", screen)
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}
	return renderProfile(p)
}

func renderProfile(p *api.Profile) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	line := func(k string, v any) { fmt.Fprintf(tw, "%s\t%v\n", k, v) }
	line("screen_name", "@"+p.ScreenName)
	line("name", p.Name)
	line("id", p.RestID)
	if p.Description != "" {
		line("bio", p.Description)
	}
	if p.Location != "" {
		line("location", p.Location)
	}
	if p.URL != "" {
		line("url", p.URL)
	}
	line("followers", p.Followers)
	line("following", p.Following)
	line("tweets", p.Tweets)
	line("verified", p.Verified)
	line("protected", p.Protected)
	line("created_at", p.CreatedAt)
	return tw.Flush()
}
