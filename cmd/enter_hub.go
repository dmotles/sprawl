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
	hubv1connect "github.com/dmotles/sprawl/internal/hub/gen/hub/v1/hubv1connect"
	"github.com/dmotles/sprawl/internal/hubtail"
	"github.com/dmotles/sprawl/internal/memory"
)

// hubDialTimeout bounds the startup registration RPC so an unreachable hub
// cannot linger.
const hubDialTimeout = 10 * time.Second

// hubTailPushTimeout bounds each PushWireLog RPC so a hung hub connection stalls
// only the tailer's own goroutine (never the live session) and lets the next
// poll retry.
const hubTailPushTimeout = 30 * time.Second

// hubTailIdentity is the fixed agent identity of the root `sprawl enter`
// session, matching the wire-log path built in
// internal/backend/claude/adapter.go.
const hubTailIdentity = "weave"

// defaultHubDialOut registers this host with the hub, if one is configured.
// It is intentionally best-effort: a missing hub URL is a clean no-op (sprawl
// runs fully offline), and any token/dial/auth failure is logged (endpoint
// host-only, token never) and swallowed. It must never return an error to the
// caller — the TUI starts regardless.
func defaultHubDialOut(getenv func(string) string, logW io.Writer, sprawlRoot string) {
	var projectURL, tokenFile string
	if cfg, err := config.Load(sprawlRoot); err == nil && cfg != nil {
		projectURL = cfg.HubURL
		tokenFile = cfg.HubTokenFile
	}
	// User-level config sits between env and project config in precedence.
	var userURL, userToken string
	if uc, err := config.LoadUserConfig(os.UserConfigDir); err == nil {
		userURL = uc.HubURL
		userToken = uc.HubToken
	}

	hubURL := hub.ResolveHubURL("", getenv, userURL, projectURL)
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
	token, err := hub.ResolveHostToken(getenv, userToken, tokenFile)
	if err != nil {
		fmt.Fprintf(logW, "[enter] hub: token resolution failed: %v (not registering)\n", err)
		return
	}
	if token == "" {
		fmt.Fprintf(logW, "[enter] hub: %s configured but no token found "+
			"(set %s, run `sprawl hub token set`, or set hub_token_file); not registering\n",
			redacted, hub.EnvHubToken)
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

	// Registration succeeded: follow the durable wire log and ship frames to the
	// hub. This blocks the dial-out goroutine for the lifetime of the process
	// (it is already fire-and-forget); it degrades silently on any hub trouble
	// and never touches the live session.
	runHostTailer(context.Background(), logW, sprawlRoot, hubURL, token, id)
}

// runHostTailer builds the PushWireLog client and tails the root session's wire
// log, shipping frames to the hub with verbatim seq. Best-effort: it resolves
// the live session id every poll (re-targeting across a fresh-session flip) and
// swallows all errors. Returns when ctx is canceled.
func runHostTailer(ctx context.Context, logW io.Writer, sprawlRoot, hubURL, token string, id hub.HostIdentity) {
	// A bounded per-RPC HTTP timeout so a hung connection cannot wedge the
	// tailer's poll loop; PushWireLog is unary, so plain HTTP/1.1 is fine.
	httpClient := &http.Client{Timeout: hubTailPushTimeout}
	client := hubv1connect.NewHubServiceClient(httpClient, hubURL)

	tailer := hubtail.New(client, hubtail.Config{
		HostID: id.HostID,
		RunID:  id.RunID,
		Bearer: token,
		Log:    logW,
	})
	resolve := func() (string, error) { return memory.ReadLastSessionID(sprawlRoot) }
	pathFor := func(sessionID string) string { return hostTailWireLogPath(sprawlRoot, sessionID) }
	tailer.Run(ctx, resolve, pathFor)
}

// hostTailWireLogPath returns the durable wire-log path for the root session,
// matching internal/backend/claude/adapter.go's construction exactly.
func hostTailWireLogPath(sprawlRoot, sessionID string) string {
	return filepath.Join(sprawlRoot, ".sprawl", "logs", "sessions", hubTailIdentity, sessionID+".ndjson")
}

// genRunID returns a short random hex id identifying this `sprawl enter` run.
func genRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "run"
	}
	return "run_" + hex.EncodeToString(b[:])
}
