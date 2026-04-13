package cmd

import (
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/x-cli/api"
	"github.com/thevibeworks/x-cli/internal/cmdutil"
)

var (
	mediaOutDir  string
	mediaQuality string
)

var mediaCmd = &cobra.Command{
	Use:   "media",
	Short: "Tweet media download (images + videos)",
}

var mediaDownloadCmd = &cobra.Command{
	Use:   "download <tweet-id|tweet-url>",
	Short: "Download all media attached to a tweet",
	Args:  cobra.ExactArgs(1),
	RunE:  runMediaDownload,
}

func init() {
	mediaDownloadCmd.Flags().StringVarP(&mediaOutDir, "out", "o", ".", "output directory")
	mediaDownloadCmd.Flags().StringVar(&mediaQuality, "quality", "large",
		"image size hint: small | medium | large | orig")
	mediaCmd.AddCommand(mediaDownloadCmd)
	rootCmd.AddCommand(mediaCmd)
}

// tweetURLRe matches both /username/status/<id> and /i/web/status/<id>.
var tweetURLRe = regexp.MustCompile(`/status/(\d+)`)

func runMediaDownload(cmd *cobra.Command, args []string) error {
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

	t, err := client.GetTweet(ctx, tweetID)
	if err != nil {
		return err
	}
	if len(t.Media) == 0 {
		cmdutil.Warn("tweet %s has no media", tweetID)
		return nil
	}

	abs, err := filepath.Abs(mediaOutDir)
	if err != nil {
		return err
	}

	results, err := client.DownloadTweetMedia(ctx, t, api.DownloadOptions{
		OutDir:  abs,
		Quality: mediaQuality,
		OnProgress: func(d api.MediaDownload) {
			cmdutil.Success("%s  %s  %s",
				d.Type,
				cmdutil.HumanCount(int(d.Bytes)),
				d.Path,
			)
		},
	})
	if err != nil {
		return err
	}
	if jsonOut {
		return cmdutil.PrintJSON(results)
	}
	cmdutil.Success("downloaded %d file(s) to %s", len(results), abs)
	return nil
}

func extractTweetID(arg string) string {
	if m := tweetURLRe.FindStringSubmatch(arg); len(m) == 2 {
		return m[1]
	}
	// Bare digits = ID.
	for _, r := range arg {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return arg
}
