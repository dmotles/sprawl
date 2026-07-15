// Command client is the QUM-871 evidence generator: it opens the server-stream
// and logs every frame arrival, gap, and disconnect with wall-clock timestamps.
// Its stdout (redirected into ./logs/, gitignored) IS the spike's evidence.
//
// Transport: against the Azure Container Apps public FQDN (https://…) the
// default HTTP/2-over-TLS client negotiates h2 via ALPN — no special config.
// Against a local h2c server (http://localhost:…) pass -insecure.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"connectrpc.com/connect"

	hubspikev1 "github.com/dmotles/sprawl/hubspike/gen/hubspike/v1"
	"github.com/dmotles/sprawl/hubspike/gen/hubspike/v1/hubspikev1connect"
)

func main() {
	addr := flag.String("addr", "http://localhost:8080", "server base URL (https://<fqdn> or http://localhost:8080)")
	fromSeq := flag.Uint64("from-seq", 0, "initial from_seq (0 = from the beginning)")
	insecure := flag.Bool("insecure", false, "use cleartext HTTP/2 (h2c) — for a local server")
	reconnect := flag.Bool("reconnect", false, "auto-reconnect on disconnect, resuming from the last seen seq")
	duration := flag.Duration("duration", 0, "stop after this long (0 = until interrupted)")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if *duration > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, *duration)
		defer stop()
	}

	httpClient := &http.Client{}
	if *insecure {
		t := &http.Transport{}
		p := new(http.Protocols)
		p.SetUnencryptedHTTP2(true)
		t.Protocols = p
		httpClient.Transport = t
	}

	client := hubspikev1connect.NewHeartbeatServiceClient(httpClient, *addr)

	last := *fromSeq
	logf("client start: addr=%s from_seq=%d insecure=%v reconnect=%v", *addr, last, *insecure, *reconnect)
	for {
		next, err := run(ctx, client, last)
		last = next
		if ctx.Err() != nil {
			logf("stopping: %v (last_seq=%d)", ctx.Err(), last)
			return
		}
		logf("DISCONNECT: %v (last_seq=%d)", err, last)
		if !*reconnect {
			return
		}
		logf("reconnecting in 2s from_seq=%d ...", last)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// run opens one stream from `last` and returns the last seq seen and the error
// that ended the stream (nil only if the context ended cleanly).
func run(ctx context.Context, client hubspikev1connect.HeartbeatServiceClient, last uint64) (uint64, error) {
	stream, err := client.Subscribe(ctx, connect.NewRequest(&hubspikev1.SubscribeRequest{FromSeq: last}))
	if err != nil {
		return last, err
	}
	logf("stream open (from_seq=%d)", last)
	for stream.Receive() {
		f := stream.Msg()
		if last != 0 && f.GetSeq() != last+1 {
			logf("GAP: got seq=%d, expected %d", f.GetSeq(), last+1)
		}
		if last != 0 && f.GetSeq() <= last {
			logf("DUPE: got seq=%d, already saw %d", f.GetSeq(), last)
		}
		rtt := time.Now().UnixMilli() - f.GetTsUnixMs()
		logf("frame seq=%d kind=%s payload=%q rtt_ms=%d", f.GetSeq(), kindName(f.GetKind()), f.GetPayload(), rtt)
		last = f.GetSeq()
	}
	return last, stream.Err()
}

func kindName(k hubspikev1.FrameKind) string {
	switch k {
	case hubspikev1.FrameKind_FRAME_KIND_HEARTBEAT:
		return "HEARTBEAT"
	case hubspikev1.FrameKind_FRAME_KIND_DATA:
		return "DATA"
	default:
		return "UNSPECIFIED"
	}
}

func logf(format string, args ...any) {
	fmt.Printf("%s "+format+"\n", append([]any{time.Now().Format(time.RFC3339Nano)}, args...)...)
}
