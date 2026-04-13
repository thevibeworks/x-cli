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
	authProfile     string
	authCookieFlag  string
	authForcePaste  bool
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage the imported X session cookie",
}

var authImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import session cookies from your browser (auto)",
	Long: `Import the auth_token and ct0 cookies from an X session.

By default, `+"`x auth import`"+` auto-detects: it scans every local
browser's cookie store on disk (Chrome, Firefox, Brave, Edge, Chromium),
decrypts the encrypted values with the per-OS Safe Storage key, and
uses whichever browser has a live x.com session. No flags, no paste.

Override only if you need to:

  --from-browser chrome    pin a specific browser instead of auto
  --cookie 'auth_token=...; ct0=...'    one-shot from a flag (scripted)
  --paste                  force the interactive paste prompt

Notes:

  - macOS prompts once for Keychain access on the first run so we can
    read the Chrome Safe Storage AES key. The system dialog says "x
    wants to access key 'Chrome' in your keychain" — that's normal.
  - Chrome must be CLOSED on macOS because it holds an exclusive lock
    on the cookie file while running. Firefox is fine while open.
  - If auto-detect finds no x.com session in any browser (typical in a
    headless container or fresh machine), x-cli falls back to the
    interactive paste prompt automatically.

x-cli stores cookies in your OS keychain via go-keyring. They are
never written to disk in plaintext.`,
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
		"pin to a specific browser: chrome | firefox | brave | edge | chromium")
	authImportCmd.Flags().StringVar(&authProfile, "profile", "",
		"pin to a specific browser profile (substring match, e.g. \"Profile 6\", \"Default\", \"work\")")
	authImportCmd.Flags().StringVar(&authCookieFlag, "cookie", "",
		"non-interactive: pass the full cookie header (visible in shell history)")
	authImportCmd.Flags().BoolVar(&authForcePaste, "paste", false,
		"skip auto-detect and prompt for an interactive paste")

	authCmd.AddCommand(authImportCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authLogoutCmd)
	authCmd.AddCommand(authBrowsersCmd)
	rootCmd.AddCommand(authCmd)
}

var authBrowsersCmd = &cobra.Command{
	Use:   "browsers",
	Short: "List local browser profiles that have an x.com session",
	Long: `Enumerate every browser cookie store on this machine that contains
at least one cookie for x.com. Use this to discover what to pass to
` + "`--from-browser`" + ` and ` + "`--profile`" + ` when ` + "`auth import`" + `'s auto-detect picks
the wrong profile.`,
	RunE: runAuthBrowsers,
}

func runAuthBrowsers(cmd *cobra.Command, _ []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	matches, err := browsercookies.List(ctx, "x.com")
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		cmdutil.Warn("no browser cookie stores have an x.com session")
		return nil
	}
	tw := cmdutil.NewTabPrinter(os.Stdout)
	for i, m := range matches {
		marker := " "
		if i == 0 {
			marker = "*" // first match is what auto-detect would pick
		}
		tw.Row(marker+" "+m.Browser, fmt.Sprintf("%-20s  %d cookie(s)  %s", m.Profile, m.Count, m.Source))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "* = auto-detect default. Override with --from-browser and --profile.")
	return nil
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
		Verbose:   verbose,
	})

	// Tight timeout for the import-time liveness check. We want
	// snappy failure on a stuck connection, not 90 seconds of silence.
	ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
	defer cancel()

	cmdutil.Info("verifying session against X (UserByRestId via twid)...")
	user, err := client.VerifyCredentials(ctx)
	if err != nil {
		return fmt.Errorf("verify session: %w (your cookies may be stale or X is unreachable)", err)
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

// acquireCookieString resolves the cookie string in priority order:
//
//  1. --cookie '...'                       (explicit one-shot)
//  2. --paste                              (explicit interactive paste)
//  3. --from-browser / --profile           (pinned browser cookie read)
//  4. (default) auto-scan, fall through to paste if empty
//
// The default mode is the only one that can SOFT-FAIL: if no browser
// has an x.com session, we silently drop to the paste prompt without
// error. Explicit modes hard-fail on their own terms.
//
// In every browser-cookie path, alternatives (other browser/profile
// pairs that also have x.com cookies) are surfaced so the user can
// re-run with `--profile` if auto-detect picked the wrong one.
func acquireCookieString() (string, error) {
	switch {
	case authCookieFlag != "":
		return strings.TrimSpace(authCookieFlag), nil

	case authForcePaste:
		return promptCookiePaste()

	case authFromBrowser != "" || authProfile != "":
		raw, err := readBrowserCookies(authFromBrowser, authProfile, false)
		return raw, err

	default:
		raw, err := readBrowserCookies("", "", true)
		if err == nil && raw != "" {
			return raw, nil
		}
		if err != nil {
			cmdutil.Warn("auto-detect: %v", err)
		}
		cmdutil.Info("falling back to interactive paste (use --from-browser / --profile to pin)")
		return promptCookiePaste()
	}
}

// readBrowserCookies runs kooky against the named browser/profile (both
// optional — empty string means "any"). softMiss controls whether the
// "no cookies found" path returns an error (false → silently miss for
// auto-detect fallthrough; true → return the error verbatim).
//
// On success it prints the chosen (browser, profile, source) and any
// alternatives so the user knows what auto-detect picked AND what
// other profiles are available.
func readBrowserCookies(browser, profile string, softMiss bool) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := browsercookies.Load(ctx, browser, profile, "x.com")
	if err != nil {
		if softMiss {
			return "", err
		}
		switch {
		case browser != "" && profile != "":
			return "", fmt.Errorf("--from-browser %s --profile %q: %w", browser, profile, err)
		case browser != "":
			return "", fmt.Errorf("--from-browser %s: %w", browser, err)
		case profile != "":
			return "", fmt.Errorf("--profile %q: %w", profile, err)
		}
		return "", err
	}

	raw := browsercookies.FormatCookieHeader(res.Cookies, cookieNamesWanted)
	if raw == "" {
		return "", fmt.Errorf("required cookies (auth_token, ct0) not in %s/%s — are you logged in to x.com on that profile?", res.Browser, res.Profile)
	}

	cmdutil.Success("using %s / %s (%s)", res.Browser, res.Profile, res.Source)
	if len(res.Alternatives) > 0 {
		cmdutil.Warn("also found x.com sessions in:")
		for _, a := range res.Alternatives {
			cmdutil.Warn("    %s / %s  (%d cookies)", a.Browser, a.Profile, a.Count)
		}
		cmdutil.Warn("re-run with --profile <name> if you want a different one")
	}
	return raw, nil
}

func promptCookiePaste() (string, error) {
	return cmdutil.ReadSecret("Paste cookie header (auth_token=...; ct0=...): ")
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
