package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/thevibeworks/x-cli/api"
	"github.com/thevibeworks/x-cli/internal/cmdutil"
	"github.com/thevibeworks/x-cli/internal/store"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose session, endpoints, throttle and egress",
	Long: `doctor runs a short set of sanity checks:

  - endpoints.yaml is loadable and sane
  - session is present and verify_credentials succeeds
  - egress IP and ASN (mutations from cloud ASNs are a ban risk)
  - how old the query IDs are (if a bundled 'updated_at' marker is present)

If any check fails, doctor exits non-zero so scripts can gate on it.`,
	RunE: runDoctor,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	ok := true

	// 1. endpoints
	eps, err := api.LoadEndpoints(resolveEndpointsPath())
	if err != nil {
		cmdutil.Fail("endpoints: %v", err)
		ok = false
	} else {
		cmdutil.Success("endpoints: %d graphql ops, %d rest ops", len(eps.GraphQL), len(eps.REST))
	}

	// 2. session
	path, perr := sessionFilePath()
	if perr != nil {
		cmdutil.Fail("session path: %v", perr)
		return perr
	}
	sess, err := store.Load(path)
	switch {
	case err != nil:
		cmdutil.Fail("session store: %v", err)
		ok = false
	case sess == nil:
		cmdutil.Warn("session: none — run 'x auth import'")
		ok = false
	default:
		cmdutil.Success("session: stored for @%s", sess.Username)
		if eps != nil {
			if err := verifyLiveSession(ctx, eps, sess); err != nil {
				cmdutil.Fail("verify_credentials: %v", err)
				ok = false
			} else {
				cmdutil.Success("verify_credentials: ok")
			}
		}
	}

	// 3. egress IP + ASN
	if ip, asn, org, err := egressInfo(ctx); err != nil {
		cmdutil.Warn("egress lookup failed: %v", err)
	} else {
		line := fmt.Sprintf("egress: %s  (%s  %s)", ip, asn, org)
		if isCloudASN(asn, org) {
			cmdutil.Warn(line + "  ← cloud ASN; mutations are high-risk from this IP")
		} else {
			cmdutil.Success(line)
		}
	}

	// 4. TLS impersonation
	//
	// v0.1 does not wrap the http.Client with a uTLS round-tripper, so our
	// TLS ClientHello does not match a real Chrome. Say so loudly — it's the
	// biggest single fingerprinting delta between x-cli and a browser.
	cmdutil.Warn("tls: no Chrome impersonation (v0.1); JA3/JA4 differs from real browser")

	if !ok {
		return fmt.Errorf("doctor reports one or more issues")
	}
	return nil
}

func verifyLiveSession(ctx context.Context, eps *api.EndpointMap, sess *store.Session) error {
	client := api.New(api.Options{
		Endpoints: eps,
		Throttle:  api.NewThrottle(api.Defaults{}),
		Session:   api.Session{Cookies: sess.Cookies},
	})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := client.VerifyCredentials(ctx)
	return err
}

// egressInfo does a single lookup to ipinfo.io. Deliberately no retries:
// this is diagnostics, not a hot path.
func egressInfo(ctx context.Context) (ip, asn, org string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://ipinfo.io/json", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	var info struct {
		IP  string `json:"ip"`
		Org string `json:"org"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", "", "", err
	}
	parts := strings.SplitN(info.Org, " ", 2)
	asn = parts[0]
	if len(parts) == 2 {
		org = parts[1]
	}
	return info.IP, asn, org, nil
}

func isCloudASN(asn, org string) bool {
	if asn == "" && org == "" {
		return false
	}
	s := strings.ToLower(asn + " " + org)
	cloud := []string{
		"amazon", "aws", "google", "microsoft", "azure", "digitalocean",
		"linode", "hetzner", "ovh", "vultr", "oracle cloud", "cloudflare",
		"scaleway", "fastly", "alibaba", "tencent",
	}
	for _, needle := range cloud {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
