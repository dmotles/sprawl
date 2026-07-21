package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
	"github.com/dmotles/sprawl/internal/hub/gen/hub/v1/hubv1connect"
)

// EnvHubToken names the environment variable that may carry the host bearer
// token directly. It takes precedence over a token file. The token is NEVER
// accepted on a CLI flag or URL (docs 04 §5) — those leak into ps/argv and the
// QUM-728 incident snapshot.
const EnvHubToken = "SPRAWL_HUB_TOKEN" //nolint:gosec // env var NAME, not a credential value

// HostIdentity is what a host announces to the hub on RegisterInstance.
type HostIdentity struct {
	HostID    string
	RunID     string
	RepoLabel string
}

// DeriveHostID computes a stable, non-reversible host id from a machine hint
// (e.g. hostname) and the sprawl root path. The hint is hashed, NOT embedded,
// so no hostname/username leaks into logs or the hub (public-repo/PII hygiene).
func DeriveHostID(sprawlRoot, machineHint string) string {
	root := sprawlRoot
	if abs, err := filepath.Abs(sprawlRoot); err == nil {
		root = abs
	}
	sum := sha256.Sum256([]byte(machineHint + "\x00" + root))
	return "host_" + hex.EncodeToString(sum[:])[:16]
}

// ResolveHostToken resolves the host bearer token from the secrets path only:
// the SPRAWL_HUB_TOKEN env var wins, else a 0600 token file. An empty result
// means "no token configured" and is returned as ("", nil) so the caller can
// treat the hub as disabled. A present-but-unreadable or wrong-mode file is an
// error.
func ResolveHostToken(getenv func(string) string, tokenFile string) (string, error) {
	if getenv != nil {
		if v := strings.TrimSpace(getenv(EnvHubToken)); v != "" {
			return v, nil
		}
	}
	if tokenFile == "" {
		return "", nil // nothing configured → hub disabled
	}
	info, err := os.Stat(tokenFile)
	if err != nil {
		return "", fmt.Errorf("hub token file: %w", err)
	}
	// The token file must be tightly scoped: mode 0600, no group/other bits.
	if perm := info.Mode().Perm(); perm != 0o600 {
		return "", fmt.Errorf("hub token file %s has mode %04o; must be 0600 "+
			"(chmod 600 the file)", tokenFile, perm)
	}
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return "", fmt.Errorf("reading hub token file: %w", err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("hub token file %s is empty", tokenFile)
	}
	return tok, nil
}

// RegisterHost dials the hub and calls RegisterInstance once with the bearer
// token on the Authorization header.
func RegisterHost(ctx context.Context, httpClient connect.HTTPClient, baseURL, bearer string, id HostIdentity) error {
	client := hubv1connect.NewHubServiceClient(httpClient, baseURL)
	req := connect.NewRequest(&hubv1.RegisterInstanceRequest{
		HostId:    id.HostID,
		RunId:     id.RunID,
		RepoLabel: id.RepoLabel,
		// user_id is intentionally omitted; the hub stamps it server-side.
	})
	req.Header().Set("Authorization", "Bearer "+bearer)
	if _, err := client.RegisterInstance(ctx, req); err != nil {
		return err
	}
	return nil
}
