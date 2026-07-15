// Command server is the QUM-871 spike downlink: a Connect server-streaming RPC
// that emits a seq'd HEARTBEAT every N seconds plus occasional DATA frames, on
// the stream itself (DATA frames, not just HTTP/2 PING — the Envoy
// stream_idle_timeout trap). It serves h2c (cleartext HTTP/2): Azure Container
// Apps' Envoy ingress terminates TLS and speaks cleartext HTTP/2 to the
// container (ingress allow_insecure_connections = true, transport = http2).
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"connectrpc.com/connect"

	hubspikev1 "github.com/dmotles/sprawl/hubspike/gen/hubspike/v1"
	"github.com/dmotles/sprawl/hubspike/gen/hubspike/v1/hubspikev1connect"
	"github.com/dmotles/sprawl/hubspike/internal/stream"
)

// dataEveryNHeartbeats emits a DATA frame once per this many heartbeats, so the
// stream carries both keep-alive heartbeats and real application frames.
const dataEveryNHeartbeats = 5

// subscriberPollInterval bounds how quickly a subscriber picks up newly appended
// frames. Small enough that heartbeat delivery latency stays well under the
// heartbeat interval; a spike simplification (no per-subscriber broadcast).
const subscriberPollInterval = 200 * time.Millisecond

// heartbeatServer streams from a process-global append-only log. The log's seq
// advances continuously (even with no subscriber), so from_seq reconnect
// replays exactly the missed frames — zero gaps, zero dupes.
type heartbeatServer struct {
	log *stream.Log
}

func (s *heartbeatServer) Subscribe(
	ctx context.Context,
	req *connect.Request[hubspikev1.SubscribeRequest],
	out *connect.ServerStream[hubspikev1.Frame],
) error {
	last := req.Msg.GetFromSeq()
	log.Printf("subscribe: from_seq=%d peer=%s", last, req.Peer().Addr)
	ticker := time.NewTicker(subscriberPollInterval)
	defer ticker.Stop()
	for {
		for _, f := range s.log.Since(last) {
			if err := out.Send(toProto(f)); err != nil {
				return err // client gone / stream cut
			}
			last = f.Seq
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func toProto(f stream.Frame) *hubspikev1.Frame {
	kind := hubspikev1.FrameKind_FRAME_KIND_HEARTBEAT
	if f.Kind == stream.KindData {
		kind = hubspikev1.FrameKind_FRAME_KIND_DATA
	}
	return &hubspikev1.Frame{Seq: f.Seq, Kind: kind, Payload: f.Payload, TsUnixMs: f.TSUnixMs}
}

// produce appends a heartbeat every interval, and a DATA frame every Nth beat.
func produce(ctx context.Context, lg *stream.Log, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	beats := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().UnixMilli()
			f := lg.Append(stream.KindHeartbeat, "hb", now)
			log.Printf("append seq=%d kind=HEARTBEAT", f.Seq)
			beats++
			if beats%dataEveryNHeartbeats == 0 {
				d := lg.Append(stream.KindData, fmt.Sprintf("data-%d", beats), time.Now().UnixMilli())
				log.Printf("append seq=%d kind=DATA", d.Seq)
			}
		}
	}
}

func heartbeatInterval() time.Duration {
	if v := os.Getenv("HEARTBEAT_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 20 * time.Second
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	interval := heartbeatInterval()

	lg := &stream.Log{}
	go produce(context.Background(), lg, interval)

	mux := http.NewServeMux()
	path, handler := hubspikev1connect.NewHeartbeatServiceHandler(&heartbeatServer{log: lg})
	mux.Handle(path, handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true) // h2c: Envoy speaks cleartext HTTP/2 to us

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		Protocols:         protocols,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("listening on :%s (h2c), heartbeat interval=%s", port, interval)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}
