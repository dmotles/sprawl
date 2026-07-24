//go:build hub_e2e

// Package hube2e — QUM-911 Hub Phase 1 capstone.
//
// TestHubLocalE2E stands up a REAL local hubd process (built from cmd/hubd,
// in-memory store, no cloud), mints a host bearer token over the real
// /login -> CreateHostToken browser flow, ships a deterministic seq'd wire-log
// through the REAL host tailer (internal/hubtail), and subscribes with the REAL
// generated Connect client as the browser stand-in. It proves:
//
//   - live_output : frames flow host -> tailer -> hub -> subscriber, verbatim seq,
//     and /debug/state reflects the live stream + subscriber.
//   - pill_state  : the session_state_changed running/idle events (the source the
//     SPA pill reduces) arrive in order.
//   - network_blip: a subscriber that drops mid-stream and resumes with
//     from_seq=lastSeq gets a contiguous delta — zero gaps, zero dupes.
//   - hubd_restart: after a real process restart (backlog + token wiped), the
//     same tailer (cursor preserved) re-uplinks only new frames and a
//     reconnecting subscriber resumes contiguously. The known P1 cold-join gap
//     (durable sink deferred, QUM-909) is asserted to document it.
//
// Behind the hub_e2e build tag so `make validate`'s plain `go test ./...` never
// spawns a child process. Run via `go test -tags hub_e2e ./internal/hub/e2e/`
// or `make test-hub-e2e` / the hub-e2e matrix row.
package hube2e

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/dmotles/sprawl/internal/hub"
	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
	"github.com/dmotles/sprawl/internal/hub/gen/hub/v1/hubv1connect"
	"github.com/dmotles/sprawl/internal/hubtail"
)

const (
	e2eHostID     = "host_e2e"
	e2eRunID      = "run_e2e"
	e2eSessionID  = "sess_e2e"
	e2eLoginToken = "devlogin-e2e"
)

// streamKey is the (host_id, run_id, session_id) triple that keys a wire-log
// stream on both the uplink and the subscribe request.
type streamKey struct{ host, run, sess string }

func e2eKey() streamKey { return streamKey{e2eHostID, e2eRunID, e2eSessionID} }

// TestHubLocalE2E must run as a whole: the subtests intentionally share mutable
// state (the wire-log file, the tailer's cursor, the hub backlog) and run in
// order, mirroring one continuous session. Later subtests assume earlier ones
// shipped their frames (e.g. hubd_restart's caughtUp==n+k assumes network_blip
// ran). Do not add t.Parallel() and do not rely on running a single subtest in
// isolation.
func TestHubLocalE2E(t *testing.T) {
	bin := buildHubd(t)
	port := freePort(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	env := hubdEnv(t)

	stop := startHubd(t, bin, port, env)
	t.Cleanup(func() { stop() })

	bearer := mintToken(t, baseURL)
	registerHost(t, baseURL, bearer)

	// One tailer for the whole test: its in-memory cursor must survive the hubd
	// restart (the real `sprawl enter` tailer lives for the process lifetime).
	// Bearer is injected by the pusher wrapper so it can be swapped after a
	// restart re-mint without rebuilding the tailer.
	pusher := &bearerPusher{inner: newHubClient(baseURL), bearer: bearer}
	tailer := hubtail.New(pusher, hubtail.Config{HostID: e2eHostID, RunID: e2eRunID, Log: io.Discard})

	wireLog := filepath.Join(t.TempDir(), "wire.ndjson")

	// --- live_output: seqs 1..5, with running (seq 2) and idle (seq 5). --------
	appendFrames(t, wireLog,
		dataFrame(1, assistantRaw("m1", "hello")),
		dataFrame(2, sessionStateRaw("running")),
		dataFrame(3, assistantRaw("m2", "working")),
		dataFrame(4, resultRaw("done")),
		dataFrame(5, sessionStateRaw("idle")),
	)
	const n = 5
	shipAll(t, tailer, wireLog)

	t.Run("live_output", func(t *testing.T) {
		frames := collectData(t, baseURL, bearer, e2eKey(), 0, n)
		seqs := seqsOf(frames)
		if err := checkContiguous(seqs, 0); err != nil {
			t.Fatalf("live replay not contiguous: %v (seqs=%v)", err, seqs)
		}
		if got := len(seqs); got != n {
			t.Fatalf("live replay frame count = %d, want %d", got, n)
		}
		// /debug/state must reflect the live stream (frame_count/last_seq).
		assertDebugStream(t, baseURL, e2eKey(), n)
	})

	t.Run("pill_state", func(t *testing.T) {
		frames := collectData(t, baseURL, bearer, e2eKey(), 0, n)
		states := sessionStatesOf(frames)
		if len(states) != 2 || states[0] != "running" || states[1] != "idle" {
			t.Fatalf("session_state sequence = %v, want [running idle]", states)
		}
	})

	// --- network_blip: subscriber caught up at N, drops, resumes from N. -------
	const k = 2 // frames pushed while the subscriber is disconnected
	t.Run("network_blip", func(t *testing.T) {
		// Subscriber A catches up to N, then "disconnects" (collectData closes
		// its stream on return).
		a := collectData(t, baseURL, bearer, e2eKey(), 0, n)
		lastSeq := seqsOf(a)[len(a)-1]
		if lastSeq != n {
			t.Fatalf("pre-blip lastSeq = %d, want %d", lastSeq, n)
		}
		// New frames arrive while A is gone.
		appendFrames(t, wireLog,
			dataFrame(6, assistantRaw("m3", "more")),
			dataFrame(7, assistantRaw("m4", "still more")),
		)
		shipAll(t, tailer, wireLog)
		// Subscriber A2 resumes from the last seq it saw.
		a2 := collectData(t, baseURL, bearer, e2eKey(), lastSeq, k)
		seqs := seqsOf(a2)
		if err := checkContiguous(seqs, lastSeq); err != nil {
			t.Fatalf("blip resume not contiguous (zero-gap/dupe violated): %v (seqs=%v)", err, seqs)
		}
		if len(seqs) != k {
			t.Fatalf("blip delta count = %d, want %d", len(seqs), k)
		}
	})

	// --- hubd_restart: real process restart, then re-uplink + resume. ----------
	const m = 10 // highest seq after the post-restart burst; N+k == 7 here
	t.Run("hubd_restart", func(t *testing.T) {
		caughtUp := int64(n + k) // 7
		// Restart the hubd process on the SAME port. Fresh memStore => backlog +
		// token wiped (documented P1 memStore behavior).
		stop()
		stop = startHubd(t, bin, port, env)
		newBearer := mintToken(t, baseURL)
		registerHost(t, baseURL, newBearer)
		pusher.setBearer(newBearer)

		// New frames after restart. The SAME tailer's cursor (== caughtUp) is
		// preserved, so it re-uplinks ONLY seq > caughtUp.
		appendFrames(t, wireLog,
			dataFrame(8, sessionStateRaw("running")),
			dataFrame(9, assistantRaw("m5", "post-restart")),
			dataFrame(10, sessionStateRaw("idle")),
		)
		shipAll(t, tailer, wireLog)

		// A subscriber that was caught up reconnects from its last-seen seq and
		// resumes with zero gaps / zero dupes across the restart seam. Note: the
		// wiped-then-rebuilt backlog holds only the re-uplinked tail (8..10), so
		// this proves "the re-uplinked tail is contiguous from caughtUp+1 with no
		// stale replay" — resume-floor FILTERING itself (a from_seq that must skip
		// frames still present in the backlog) is exercised by network_blip above,
		// where the pre-restart backlog still retains 1..7.
		want := int(m - caughtUp) // 3
		reconnect := collectData(t, baseURL, newBearer, e2eKey(), caughtUp, want)
		seqs := seqsOf(reconnect)
		if err := checkContiguous(seqs, caughtUp); err != nil {
			t.Fatalf("restart resume not contiguous (zero-gap/dupe violated): %v (seqs=%v)", err, seqs)
		}
		if len(seqs) != want {
			t.Fatalf("restart delta count = %d, want %d", len(seqs), want)
		}

		// Documented P1 limitation: a FRESH cold-join (from_seq=0) after restart
		// only sees the re-uplinked tail, not the full history — the durable sink
		// is deferred (QUM-909), so the rebuilt backlog starts at caughtUp+1.
		cold := collectData(t, baseURL, newBearer, e2eKey(), 0, want)
		coldSeqs := seqsOf(cold)
		if len(coldSeqs) == 0 || coldSeqs[0] != caughtUp+1 {
			t.Fatalf("cold-join-after-restart first seq = %v, want %d "+
				"(documents the P1 durable-sink-deferred gap)", coldSeqs, caughtUp+1)
		}
	})
}

// --- helpers ---------------------------------------------------------------

// buildHubd compiles cmd/hubd into a temp dir and returns the binary path.
func buildHubd(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "hubd")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/dmotles/sprawl/cmd/hubd")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build hubd: %v\n%s", err, out)
	}
	return bin
}

// hubdEnv builds the child environment: in-memory store (no DSN), debug endpoint
// on, browser login enabled (login token + cookie key), a stable secret keeper,
// and text logs for readable child output on failure.
func hubdEnv(t *testing.T) []string {
	t.Helper()
	return append(os.Environ(),
		"SPRAWL_HUB_LOG_FORMAT=text",
		"SPRAWL_HUB_DEBUG_ENDPOINT=1",
		"SPRAWL_HUB_LOGIN_TOKEN="+e2eLoginToken,
		"SPRAWL_HUB_COOKIE_KEY="+randB64(t, 32),
		"SPRAWL_HUB_SECRET_URL=base64key://"+randB64(t, 32),
	)
}

// startHubd launches the hubd binary on 127.0.0.1:port and blocks until it is
// serving (/healthz==200). It returns a stop func that kills the process and
// waits for it to exit. Child stdout/stderr is captured and dumped on a startup
// failure.
func startHubd(t *testing.T, bin string, port int, env []string) (stop func()) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	cmd := exec.Command(bin, "-addr", addr)
	cmd.Env = env
	var buf syncBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start hubd: %v", err)
	}
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	baseURL := "http://" + addr
	if !waitReady(baseURL, 20*time.Second) {
		out := buf.String()
		stop()
		t.Fatalf("hubd did not become ready at %s\n--- child output ---\n%s", baseURL, out)
	}
	return stop
}

// waitReady polls /healthz until it returns 200 or the deadline passes.
func waitReady(baseURL string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// mintToken performs the real browser token-mint flow: POST /login with the
// login token, capture the signed session cookie from the 303 response (Go's
// cookie jar drops the Secure cookie over http, so we replay it by hand), then
// call CreateHostToken authenticated by that cookie and return the plaintext
// bearer.
func mintToken(t *testing.T, baseURL string) string {
	t.Helper()
	// Stop at the 303 so we can read Set-Cookie off the login response itself.
	loginClient := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	form := url.Values{"token": {e2eLoginToken}}
	resp, err := loginClient.PostForm(baseURL+"/login", form)
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /login status = %d, want 303", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "sprawl_hub_session" {
			cookie = c
			break
		}
	}
	if cookie == nil {
		t.Fatalf("POST /login set no sprawl_hub_session cookie")
	}

	client := newHubClient(baseURL)
	req := connect.NewRequest(&hubv1.CreateHostTokenRequest{Label: "e2e"})
	req.Header().Set("Cookie", cookie.Name+"="+cookie.Value)
	tokResp, err := client.CreateHostToken(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateHostToken: %v", err)
	}
	tok := tokResp.Msg.GetToken()
	if tok == "" {
		t.Fatalf("CreateHostToken returned empty token")
	}
	return tok
}

// registerHost calls the real RegisterInstance RPC so /debug/state reflects the
// instance (and to exercise the register path end to end).
func registerHost(t *testing.T, baseURL, bearer string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id := hub.HostIdentity{HostID: e2eHostID, RunID: e2eRunID, RepoLabel: "e2e"}
	if err := hub.RegisterHost(ctx, &http.Client{Transport: noKeepAlive()}, baseURL, bearer, id); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
}

// shipAll drives the real tailer to ship every not-yet-shipped frame from path.
func shipAll(t *testing.T, tailer *hubtail.Tailer, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := tailer.ShipSession(ctx, e2eSessionID, path); err != nil {
		t.Fatalf("tailer.ShipSession: %v", err)
	}
}

// collectData opens a SubscribeWireLog stream (bearer-authed) at fromSeq and
// returns the first `want` DATA frames (heartbeats skipped). It fails the test
// if `want` frames do not arrive within a bounded window.
func collectData(t *testing.T, baseURL, bearer string, key streamKey, fromSeq int64, want int) []*hubv1.WireFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := newHubClient(baseURL)
	req := connect.NewRequest(&hubv1.SubscribeWireLogRequest{
		HostId: key.host, RunId: key.run, SessionId: key.sess, FromSeq: fromSeq,
	})
	req.Header().Set("Authorization", "Bearer "+bearer)
	stream, err := client.SubscribeWireLog(ctx, req)
	if err != nil {
		t.Fatalf("SubscribeWireLog(fromSeq=%d): %v", fromSeq, err)
	}
	defer func() { _ = stream.Close() }()
	out := make([]*hubv1.WireFrame, 0, want)
	for len(out) < want && stream.Receive() {
		fr := stream.Msg().GetFrame()
		if fr.GetKind() == hubv1.WireFrameKind_WIRE_FRAME_KIND_HEARTBEAT {
			continue
		}
		out = append(out, fr)
	}
	if len(out) < want {
		t.Fatalf("collectData(fromSeq=%d) got %d DATA frames, want %d: %v",
			fromSeq, len(out), want, stream.Err())
	}
	return out
}

// assertDebugStream polls /debug/state until it reports our session with the
// expected backlog frame count (and matching last_seq).
func assertDebugStream(t *testing.T, baseURL string, key streamKey, wantFrames int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	client := &http.Client{Timeout: time.Second}
	var last string
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/debug/state")
		if err != nil {
			t.Fatalf("GET /debug/state: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		last = string(body)
		var snap struct {
			Streams []struct {
				HostID    string `json:"host_id"`
				RunID     string `json:"run_id"`
				SessionID string `json:"session_id"`
				Framen    int    `json:"frame_count"`
				LastSeq   int64  `json:"last_seq"`
			} `json:"streams"`
		}
		if err := json.Unmarshal(body, &snap); err == nil {
			for _, s := range snap.Streams {
				// frame_count == last_seq only because this fixture is a gapless
				// 1..N stream; the two quantities are distinct in general.
				if s.HostID == key.host && s.RunID == key.run && s.SessionID == key.sess &&
					s.Framen == wantFrames && s.LastSeq == int64(wantFrames) {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("/debug/state never reported stream %v with frame_count=%d; last body:\n%s", key, wantFrames, last)
}

// bearerPusher wraps the generated client to inject a mutable bearer token on
// every PushWireLog, so the token can be swapped after a hubd restart re-mint
// without rebuilding the (cursor-bearing) tailer.
type bearerPusher struct {
	inner  hubv1connect.HubServiceClient
	mu     sync.Mutex
	bearer string
}

func (p *bearerPusher) PushWireLog(ctx context.Context, req *connect.Request[hubv1.PushWireLogRequest]) (*connect.Response[hubv1.PushWireLogResponse], error) {
	p.mu.Lock()
	b := p.bearer
	p.mu.Unlock()
	req.Header().Set("Authorization", "Bearer "+b)
	return p.inner.PushWireLog(ctx, req)
}

func (p *bearerPusher) setBearer(b string) {
	p.mu.Lock()
	p.bearer = b
	p.mu.Unlock()
}

// newHubClient builds a Connect client with keep-alives disabled so a stale
// connection can never survive a hubd process restart on the same port.
func newHubClient(baseURL string) hubv1connect.HubServiceClient {
	return hubv1connect.NewHubServiceClient(&http.Client{Transport: noKeepAlive()}, baseURL)
}

func noKeepAlive() *http.Transport {
	return &http.Transport{DisableKeepAlives: true}
}

// --- wire-log fixture ------------------------------------------------------

// wireEnv is one NDJSON wire-log envelope. The json tags mirror
// internal/transcript.Envelope's on-disk contract (ts/dir/seq/raw) exactly — we
// pin the raw NDJSON shape here deliberately so this e2e also guards that
// contract, rather than importing the struct.
type wireEnv struct {
	TS  string `json:"ts"`
	Dir string `json:"dir"`
	Seq int64  `json:"seq"`
	Raw string `json:"raw"`
}

func dataFrame(seq int64, raw string) wireEnv {
	return wireEnv{TS: time.Now().UTC().Format(time.RFC3339Nano), Dir: "out", Seq: seq, Raw: raw}
}

func assistantRaw(id, text string) string {
	return fmt.Sprintf(
		`{"type":"assistant","message":{"id":%q,"role":"assistant","model":"e2e","content":[{"type":"text","text":%q}]}}`+"\n",
		id, text)
}

func resultRaw(text string) string {
	return fmt.Sprintf(`{"type":"result","subtype":"success","is_error":false,"result":%q}`+"\n", text)
}

func sessionStateRaw(state string) string {
	return fmt.Sprintf(`{"type":"system","subtype":"session_state_changed","state":%q}`+"\n", state)
}

// appendFrames appends NDJSON envelopes to path, mirroring the frame-oriented
// wire-log writer (one '\n'-terminated envelope per frame).
func appendFrames(t *testing.T, path string, envs ...wireEnv) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open wire log: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range envs {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encode envelope: %v", err)
		}
	}
}

func seqsOf(frames []*hubv1.WireFrame) []int64 {
	out := make([]int64, len(frames))
	for i, fr := range frames {
		out[i] = fr.GetSeq()
	}
	return out
}

// sessionStatesOf extracts, in order, the states of any session_state_changed
// events carried in the DATA frames' raw payloads.
func sessionStatesOf(frames []*hubv1.WireFrame) []string {
	var states []string
	for _, fr := range frames {
		var ev struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			State   string `json:"state"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(fr.GetRaw())), &ev); err != nil {
			continue
		}
		if ev.Type == "system" && ev.Subtype == "session_state_changed" {
			states = append(states, ev.State)
		}
	}
	return states
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func randB64(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// syncBuffer is a minimal concurrency-safe buffer for capturing child output
// written from the exec pipe goroutine while the test reads it on failure.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
