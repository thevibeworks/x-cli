package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/thevibeworks/x-cli/api"
	"github.com/thevibeworks/x-cli/internal/cmdutil"
	"github.com/thevibeworks/x-cli/internal/store"
	"github.com/thevibeworks/x-cli/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile       string
	endpointsFile string
	jsonOut       bool
	verbose       bool
)

var rootCmd = &cobra.Command{
	Use:   "x",
	Short: "x — small CLI for scraping and lightly automating X",
	Long: `x is a thin command-line client for X (formerly Twitter).

It talks to X's internal web endpoints using a cookie imported from your
real browser session. All commands respect a built-in throttle; growth
mutations require --apply and are dry-run by default.

This is not an official X client. Your account can be rate-limited or
suspended if you misuse it. See 'x doctor' and the skill reference.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Version:       version.Version,
}

// Execute is the entry point called from main.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		cmdutil.Fail("%v", err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default $HOME/.config/x-cli/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&endpointsFile, "endpoints", "", "endpoints.yaml (default $HOME/.config/x-cli/endpoints.yaml, then ./endpoints.yaml)")
	rootCmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON on stdout")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose logging to stderr")
}

func initConfig() {
	viper.SetEnvPrefix("X_CLI")
	viper.AutomaticEnv()

	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		if dir, err := configDir(); err == nil {
			viper.AddConfigPath(dir)
		}
		viper.AddConfigPath(".")
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
	}
	_ = viper.ReadInConfig()
}

// -----------------------------------------------------------------------------
// Shared helpers used by every subcommand.
// -----------------------------------------------------------------------------

// configDir returns the x-cli config directory using the platform-native
// user config location:
//   - Linux:   $XDG_CONFIG_HOME/x-cli or ~/.config/x-cli
//   - macOS:   ~/Library/Application Support/x-cli
//   - Windows: %AppData%/x-cli
//
// It returns an error if os.UserConfigDir cannot resolve a base — callers
// should treat that as fatal for any command that needs persistent state.
func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(base, "x-cli"), nil
}

// resolveEndpointsPath picks the first existing endpoints.yaml in this order:
//  1. --endpoints flag
//  2. <configDir>/endpoints.yaml
//  3. ./endpoints.yaml next to the binary
func resolveEndpointsPath() string {
	if endpointsFile != "" {
		return endpointsFile
	}
	candidates := []string{}
	if dir, err := configDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, "endpoints.yaml"))
	}
	candidates = append(candidates, "endpoints.yaml")
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return candidates[0]
}

func sessionFilePath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "session.enc"), nil
}

// newClient loads endpoints + stored session and returns a ready client.
// Subcommands that don't need an authenticated session can construct their
// own client without going through this helper.
func newClient(ctx context.Context) (*api.Client, error) {
	eps, err := api.LoadEndpoints(resolveEndpointsPath())
	if err != nil {
		return nil, fmt.Errorf("load endpoints: %w", err)
	}

	path, err := sessionFilePath()
	if err != nil {
		return nil, err
	}
	sess, err := store.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	if sess == nil || len(sess.Cookies) == 0 {
		return nil, fmt.Errorf("no session found — run 'x auth import' first")
	}
	if err := api.RequireAuthCookies(sess.Cookies); err != nil {
		return nil, err
	}

	throttle := api.NewThrottle(api.Defaults{
		ReadRPS:          viper.GetFloat64("throttle.read_rps"),
		ReadBurst:        viper.GetInt("throttle.read_burst"),
		MutationMinGap:   viper.GetDuration("throttle.mutation_min_gap"),
		MutationMaxGap:   viper.GetDuration("throttle.mutation_max_gap"),
		MutationDailyCap: viper.GetInt("throttle.mutation_daily_cap"),
		AutopauseAfter:   viper.GetInt("throttle.autopause_on_error_cluster"),
	})

	client := api.New(api.Options{
		Endpoints: eps,
		Throttle:  throttle,
		Session: api.Session{
			Cookies: sess.Cookies,
			User: &api.User{
				ID:       sess.UserID,
				Username: sess.Username,
				Name:     sess.Name,
			},
		},
	})
	return client, nil
}

// withTimeout wraps a context with a sensible default for CLI operations.
func withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 2*time.Minute)
}
