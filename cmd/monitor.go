package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/x-cli/api"
	"github.com/thevibeworks/x-cli/internal/cmdutil"
)

var (
	monitorInterval time.Duration
	monitorOnce     bool
)

var monitorCmd = &cobra.Command{
	Use:   "monitor",
	Short: "Live monitoring",
}

var monitorAccountCmd = &cobra.Command{
	Use:   "account <screen-name>",
	Short: "Poll a user, stream new tweets and follower delta",
	Args:  cobra.ExactArgs(1),
	RunE:  runMonitorAccount,
}

func init() {
	monitorAccountCmd.Flags().DurationVarP(&monitorInterval, "interval", "i", 60*time.Second,
		"poll interval (minimum 15s)")
	monitorAccountCmd.Flags().BoolVar(&monitorOnce, "once", false, "snapshot once and exit")
	monitorCmd.AddCommand(monitorAccountCmd)
	rootCmd.AddCommand(monitorCmd)
}

func runMonitorAccount(cmd *cobra.Command, args []string) error {
	screen := strings.TrimPrefix(args[0], "@")

	interval := monitorInterval
	if interval < 15*time.Second {
		cmdutil.Warn("interval clamped to 15s (you passed %v)", interval)
		interval = 15 * time.Second
	}

	client, err := newClient(cmd.Context())
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	known := map[string]struct{}{}
	var lastFollowers int
	first := true

	tick := func() error {
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		profile, err := client.GetProfile(callCtx, screen)
		if err != nil {
			return err
		}
		tweets, err := client.UserTweets(callCtx, screen, api.TimelineOptions{Limit: 20})
		if err != nil {
			return err
		}

		if first {
			cmdutil.Success("monitoring @%s — %s followers, %d tweets known",
				profile.ScreenName, cmdutil.HumanCount(profile.Followers), len(tweets))
			lastFollowers = profile.Followers
			for _, t := range tweets {
				known[t.ID] = struct{}{}
			}
			first = false
			return nil
		}

		if profile.Followers != lastFollowers {
			delta := profile.Followers - lastFollowers
			sign := "+"
			if delta < 0 {
				sign = ""
			}
			cmdutil.Info("followers %s%d → %d", sign, delta, profile.Followers)
			lastFollowers = profile.Followers
		}

		for _, t := range tweets {
			if _, seen := known[t.ID]; seen {
				continue
			}
			known[t.ID] = struct{}{}
			fmt.Fprintf(os.Stdout, "%s  %s  %s\n",
				time.Now().Format("15:04:05"),
				t.ID,
				cmdutil.TruncateRunes(cmdutil.SingleLine(t.Text), 100),
			)
		}
		return nil
	}

	if monitorOnce {
		return tick()
	}
	if err := tick(); err != nil {
		return err
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := tick(); err != nil {
				cmdutil.Fail("%v", err)
			}
		}
	}
}
