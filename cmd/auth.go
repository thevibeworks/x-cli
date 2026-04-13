package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/x-cli/api"
	"github.com/thevibeworks/x-cli/internal/browsercookies"
	"github.com/thevibeworks/x-cli/internal/cmdutil"
	"github.com/thevibeworks/x-cli/internal/store"
)

var (
	authFromBrowser string
	authCookieFlag  string
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage the imported X session cookie",
}

var authImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import session cookies from your browser",
	Long: `Import the auth_token and ct0 cookies from an X session.

Three import modes:

  1. Auto from a local browser  (no manual paste — recommended)

       x auth import --from-browser chrome
       x auth import --from-browser firefox
       x auth import --from-browser brave
       x auth import --from-browser edge

     Reads the cookie store directly from disk and decrypts the values
     using the browser's per-OS Safe Storage key. macOS may prompt
     once for Keychain access. Chrome must be CLOSED on macOS — it
     locks the cookie file while running. Firefox usually works while
     open.

  2. One-shot from a flag  (scripted setups)

       x auth import --cookie 'auth_token=...; ct0=...; twid=u%3D...'

     Pass the full cookie header on the command line. Visible in
     shell history; prefer --from-browser or stdin paste for normal use.

  3. Interactive paste  (default — works everywhere)

       x auth import
       # paste at the prompt: auth_token=...; ct0=...; twid=u%3D...

     Open x.com in your real browser, DevTools → Application → Cookies
     → https://x.com, copy auth_token + ct0, paste here.

In all three modes, x-cli stores the cookies in your OS keychain via
go-keyring. They are never written to disk in plaintext.`,
	RunE: runAuthImport,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the current session and verify it with X",
	RunE:  runAuthStatus,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove the stored session",
	RunE:  runAuthLogout,
}

func init() {
	authImportCmd.Flags().StringVar(&authFromBrowser, "from-browser", "",
		"read cookies directly from a local browser: chrome | firefox | brave | edge | chromium")
	authImportCmd.Flags().StringVar(&authCookieFlag, "cookie", "",
		"non-interactive: pass the full cookie header as a flag (visible in shell history)")

	authCmd.AddCommand(authImportCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authLogoutCmd)
	rootCmd.AddCommand(authCmd)
}

// cookieNamesWanted is the subset of browser cookies x-cli imports.
// auth_token and ct0 are required; twid carries the user id so we can
// skip a UserByScreenName roundtrip on `auth status`; the others are
// helpful for stable header building but not strictly required.
var cookieNamesWanted = []string{"auth_token", "ct0", "twid", "kdt", "att", "guest_id"}

func runAuthImport(cmd *cobra.Command, _ []string) error {
	cmdutil.Warn("Reminder: x-cli uses your real logged-in session. Automation can get")
	cmdutil.Warn("your account rate-limited or suspended. Read skills/x-cli/references/auth.md.")

	raw, err := acquireCookieString()
	if err != nil {
		return err
	}
	if raw == "" {
		return fmt.Errorf("no cookie provided")
	}

	cookies := api.ParseCookieString(raw)
	if err := api.RequireAuthCookies(cookies); err != nil {
		return err
	}

	eps, err := api.LoadEndpoints(resolveEndpointsPath())
	if err != nil {
		return err
	}
	client := api.New(api.Options{
		Endpoints: eps,
		Throttle:  api.NewThrottle(api.Defaults{}),
		Session:   api.Session{Cookies: cookies},
	})

	ctx, cancel := withTimeout(context.Background())
	defer cancel()

	user, err := client.VerifyCredentials(ctx)
	if err != nil {
		return fmt.Errorf("verify session: %w", err)
	}

	path, err := sessionFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	s := &store.Session{
		Cookies:  cookies,
		UserID:   user.ID,
		Username: user.Username,
		Name:     user.Name,
	}
	if err := store.Save(s, path); err != nil {
		return fmt.Errorf("save session: %w", err)
	}

	cmdutil.Success("logged in as @%s (%s)", user.Username, user.Name)
	return nil
}

// acquireCookieString returns the cookie header string from one of the
// three import modes (--from-browser, --cookie, interactive paste).
// The three modes are mutually exclusive and checked in priority order.
func acquireCookieString() (string, error) {
	switch {
	case authFromBrowser != "":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := browsercookies.Load(ctx, authFromBrowser, "x.com")
		if err != nil {
			return "", fmt.Errorf("--from-browser %s: %w", authFromBrowser, err)
		}
		raw := browsercookies.FormatCookieHeader(res.Cookies, cookieNamesWanted)
		if raw == "" {
			return "", fmt.Errorf("--from-browser %s: required cookies (auth_token, ct0) not found", authFromBrowser)
		}
		cmdutil.Info("loaded %d x.com cookie(s) from %s (%s)",
			countCookies(res.Cookies, cookieNamesWanted), res.Browser, res.Source)
		return raw, nil

	case authCookieFlag != "":
		return strings.TrimSpace(authCookieFlag), nil

	default:
		return cmdutil.ReadSecret("Paste cookie header (auth_token=...; ct0=...): ")
	}
}

// countCookies returns how many of the wanted names are present in the
// cookie map. Used only for the friendly log line.
func countCookies(cookies map[string]string, wanted []string) int {
	n := 0
	for _, k := range wanted {
		if v, ok := cookies[k]; ok && v != "" {
			n++
		}
	}
	return n
}

func runAuthStatus(cmd *cobra.Command, _ []string) error {
	path, err := sessionFilePath()
	if err != nil {
		return err
	}
	s, err := store.Load(path)
	if err != nil {
		return err
	}
	if s == nil {
		cmdutil.Warn("no session stored — run 'x auth import'")
		return nil
	}

	client, err := newClient(cmd.Context())
	if err != nil {
		return err
	}

	ctx, cancel := withTimeout(cmd.Context())
	defer cancel()

	user, err := client.VerifyCredentials(ctx)
	if err != nil {
		cmdutil.Fail("session invalid: %v", err)
		return err
	}
	cmdutil.Success("session ok — @%s (id=%s)", user.Username, user.ID)
	return nil
}

func runAuthLogout(cmd *cobra.Command, _ []string) error {
	path, err := sessionFilePath()
	if err != nil {
		return err
	}
	if err := store.Delete(path); err != nil {
		return err
	}
	cmdutil.Success("session removed")
	return nil
}
