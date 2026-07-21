package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/hub"
)

// hubDialTimeout bounds the startup registration RPC so an unreachable hub
// cannot linger.
const hubDialTimeout = 10 * time.Second

// defaultHubDialOut registers this host with the hub, if one is configured.
// It is intentionally best-effort: a missing hub URL is a clean no-op (sprawl
// runs fully offline), and any token/dial/auth failure is logged (endpoint
// host-only, token never) and swallowed. It must never return an error to the
// caller — the TUI starts regardless.
func defaultHubDialOut(getenv func(string) string, logW io.Writer, sprawlRoot string) {
	var configURL, tokenFile string
	if cfg, err := config.Load(sprawlRoot); err == nil && cfg != nil {
		configURL = cfg.HubURL
		tokenFile = cfg.HubTokenFile
	}

	hubURL := hub.ResolveHubURL("", getenv, configURL)
	if hubURL == "" {
		return // no hub configured → offline no-op
	}
	// connect-go needs a scheme; default to cleartext h2c for a local hub.
	if !strings.Contains(hubURL, "://") {
		hubURL = "http://" + hubURL
	}
	redacted := hub.RedactHubURL(hubURL)

	// Resolve the token file relative to the sprawl root when not absolute.
	if tokenFile != "" && !filepath.IsAbs(tokenFile) {
		tokenFile = filepath.Join(sprawlRoot, tokenFile)
	}
	token, err := hub.ResolveHostToken(getenv, tokenFile)
	if err != nil {
		fmt.Fprintf(logW, "[enter] hub: token resolution failed: %v (not registering)\n", err)
		return
	}
	if token == "" {
		fmt.Fprintf(logW, "[enter] hub: %s configured but no token found "+
			"(set %s or hub_token_file); not registering\n", redacted, hub.EnvHubToken)
		return
	}

	hostname, _ := os.Hostname()
	id := hub.HostIdentity{
		HostID:    hub.DeriveHostID(sprawlRoot, hostname),
		RunID:     genRunID(),
		RepoLabel: filepath.Base(sprawlRoot),
	}

	ctx, cancel := context.WithTimeout(context.Background(), hubDialTimeout)
	defer cancel()
	if err := hub.RegisterHost(ctx, http.DefaultClient, hubURL, token, id); err != nil {
		fmt.Fprintf(logW, "[enter] hub: registration with %s failed: %v (continuing offline)\n", redacted, err)
		return
	}
	fmt.Fprintf(logW, "[enter] hub: registered with %s as %s\n", redacted, id.HostID)
}

// genRunID returns a short random hex id identifying this `sprawl enter` run.
func genRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "run"
	}
	return "run_" + hex.EncodeToString(b[:])
}
