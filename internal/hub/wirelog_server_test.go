package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
	"github.com/dmotles/sprawl/internal/hub/gen/hub/v1/hubv1connect"
)

func wireFrames(seqs ...int64) []*hubv1.WireFrame {
	out := make([]*hubv1.WireFrame, 0, len(seqs))
	for _, s := range seqs {
		out = append(out, &hubv1.WireFrame{
			Seq:       s,
			Direction: hubv1.WireDirection_WIRE_DIRECTION_OUT,
			Kind:      hubv1.WireFrameKind_WIRE_FRAME_KIND_DATA,
			Raw:       "frame",
			TsUnixMs:  s,
		})
	}
	return out
}

// recvData pulls the next non-heartbeat frame from a client stream, failing if
// the stream ends first.
func recvData(t *testing.T, stream *connect.ServerStreamForClient[hubv1.SubscribeWireLogResponse]) *hubv1.WireFrame {
	t.Helper()
	for stream.Receive() {
		fr := stream.Msg().GetFrame()
		if fr.GetKind() == hubv1.WireFrameKind_WIRE_FRAME_KIND_HEARTBEAT {
			continue
		}
		return fr
	}
	t.Fatalf("stream ended before a data frame: %v", stream.Err())
	return nil
}

func TestPushWireLog_RequiresIDs(t *testing.T) {
	client, plaintext, closeFn := newAuthedHubServer(t, false)
	defer closeFn()

	cases := []struct {
		name            string
		host, run, sess string
	}{
		{"missing host", "", "r", "s"},
		{"missing run", "h", "", "s"},
		{"missing session", "h", "r", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.PushWireLog(context.Background(), withBearer(
				connect.NewRequest(&hubv1.PushWireLogRequest{
					HostId: tc.host, RunId: tc.run, SessionId: tc.sess,
					Frames: wireFrames(1),
				}), plaintext))
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("code = %v, want InvalidArgument", connect.CodeOf(err))
			}
		})
	}
}

func TestPushWireLog_RejectsMissingToken(t *testing.T) {
	client, _, closeFn := newAuthedHubServer(t, false)
	defer closeFn()
	_, err := client.PushWireLog(context.Background(),
		connect.NewRequest(&hubv1.PushWireLogRequest{HostId: "h", RunId: "r", SessionId: "s"}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

// TestSubscribeWireLog_ReplayThenLive drives the whole wire: push a backlog,
// subscribe from 0 and replay it, then push a live frame and receive it.
func TestSubscribeWireLog_ReplayThenLive(t *testing.T) {
	client, plaintext, closeFn := newAuthedHubServer(t, false)
	defer closeFn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	push := func(seqs ...int64) {
		t.Helper()
		if _, err := client.PushWireLog(ctx, withBearer(
			connect.NewRequest(&hubv1.PushWireLogRequest{
				HostId: "h", RunId: "r", SessionId: "s", Frames: wireFrames(seqs...),
			}), plaintext)); err != nil {
			t.Fatalf("PushWireLog: %v", err)
		}
	}

	push(1, 2)
	stream, err := client.SubscribeWireLog(ctx, withBearer(
		connect.NewRequest(&hubv1.SubscribeWireLogRequest{
			HostId: "h", RunId: "r", SessionId: "s", FromSeq: 0,
		}), plaintext))
	if err != nil {
		t.Fatalf("SubscribeWireLog: %v", err)
	}

	for _, want := range []int64{1, 2} {
		if got := recvData(t, stream).GetSeq(); got != want {
			t.Fatalf("replay seq = %d, want %d", got, want)
		}
	}
	push(3)
	if got := recvData(t, stream).GetSeq(); got != 3 {
		t.Fatalf("live seq = %d, want 3", got)
	}
}

// TestSubscribeWireLog_RejectsMissingToken proves the streaming handler is
// authenticated (a plain UnaryInterceptorFunc would leave it wide open).
func TestSubscribeWireLog_RejectsMissingToken(t *testing.T) {
	client, _, closeFn := newAuthedHubServer(t, false)
	defer closeFn()

	stream, err := client.SubscribeWireLog(context.Background(),
		connect.NewRequest(&hubv1.SubscribeWireLogRequest{HostId: "h", RunId: "r", SessionId: "s"}))
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
		}
		return
	}
	// Error may surface on first Receive for a server stream.
	if stream.Receive() {
		t.Fatal("unauthenticated subscribe received a frame; want rejection")
	}
	if connect.CodeOf(stream.Err()) != connect.CodeUnauthenticated {
		t.Fatalf("stream err code = %v, want Unauthenticated", connect.CodeOf(stream.Err()))
	}
}

// TestDebugState_ShowsStreamsAndConnections (AC4): after ingest + an open
// subscriber, /debug/state reports live stream and connection state.
func TestDebugState_ShowsStreamsAndConnections(t *testing.T) {
	st := newMemStore(t)
	plaintext := seedToken(t, st)
	srv := NewServer(HubConfig{Store: st, DebugEndpoint: true})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := hubv1connect.NewHubServiceClient(ts.Client(), ts.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := client.PushWireLog(ctx, withBearer(
		connect.NewRequest(&hubv1.PushWireLogRequest{
			HostId: "h", RunId: "r", SessionId: "s", Frames: wireFrames(1, 2),
		}), plaintext)); err != nil {
		t.Fatalf("PushWireLog: %v", err)
	}

	// Open a subscriber and keep draining in the background.
	go func() {
		stream, err := client.SubscribeWireLog(ctx, withBearer(
			connect.NewRequest(&hubv1.SubscribeWireLogRequest{
				HostId: "h", RunId: "r", SessionId: "s", FromSeq: 0,
			}), plaintext))
		if err != nil {
			return
		}
		for stream.Receive() {
		}
	}()

	// Poll until the snapshot reflects the stream and the live subscriber.
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := http.Get(ts.URL + "/debug/state")
		if err != nil {
			t.Fatalf("GET /debug/state: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var snap map[string]any
		if err := json.Unmarshal(body, &snap); err != nil {
			t.Fatalf("decode snapshot: %v (body=%s)", err, body)
		}
		streams, _ := snap["streams"].([]any)
		connections, _ := snap["connections"].([]any)
		if len(streams) == 1 && len(connections) == 1 {
			s := streams[0].(map[string]any)
			c := connections[0].(map[string]any)
			if s["session_id"] == "s" && s["frame_count"].(float64) == 2 &&
				c["subscribers"].(float64) >= 1 {
				return // AC4 satisfied
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("debug snapshot never reflected stream+subscriber: %s", body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
