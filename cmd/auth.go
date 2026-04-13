package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thevibeworks/x-cli/api"
	"github.com/thevibeworks/x-cli/internal/cmdutil"
	"github.com/thevibeworks/x-cli/internal/store"
	"github.com/spf13/cobra"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage the imported X session cookie",
}

var authImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import session cookies from your browser",
	Long: `Import the auth_token and ct0 cookies from a browser session on x.com.

  1. Open x.com in your real browser and log in.
  2. DevTools → Application → Cookies → https://x.com
  3. Copy the values for auth_token and ct0
  4. Run:  x auth import
     and paste:  auth_token=...; ct0=...; twid=u%3D...

The cookies are stored in the OS keychain when available and in an
AES-GCM encrypted file otherwise. They are never written in plaintext.`,
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
	authCmd.AddCommand(authImportCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authLogoutCmd)
	rootCmd.AddCommand(authCmd)
}

func runAuthImport(cmd *cobra.Command, _ []string) error {
	cmdutil.Warn("Reminder: x-cli uses your real logged-in session. Automation can get")
	cmdutil.Warn("your account rate-limited or suspended. Read skills/x-cli/references/auth.md.")

	raw, err := cmdutil.ReadSecret("Paste cookie header (auth_token=...; ct0=...): ")
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
