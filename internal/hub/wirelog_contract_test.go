package hub

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
	"github.com/dmotles/sprawl/internal/hub/gen/hub/v1/hubv1connect"
)

// TestWireLogContractShape locks the QUM-907 wire-log message shape: a seq'd
// frame carrying direction/kind/raw/ts, an uplink batch request, and a
// browser-subscribe request + streamed response wrapper. Referencing every
// symbol makes this fail to compile until the proto is added and regenerated.
func TestWireLogContractShape(t *testing.T) {
	// WireFrame carries the verbatim QUM-902 seq plus direction, kind, raw, ts.
	f := &hubv1.WireFrame{
		Seq:       42,
		Direction: hubv1.WireDirection_WIRE_DIRECTION_OUT,
		Kind:      hubv1.WireFrameKind_WIRE_FRAME_KIND_DATA,
		Raw:       `{"type":"assistant"}`,
		TsUnixMs:  1,
	}
	if f.GetSeq() != 42 {
		t.Fatalf("seq roundtrip: got %d", f.GetSeq())
	}
	if f.GetDirection() != hubv1.WireDirection_WIRE_DIRECTION_OUT {
		t.Fatalf("direction roundtrip: got %v", f.GetDirection())
	}

	// Uplink request: host_id/run_id/session_id/frames[]/from_seq.
	push := &hubv1.PushWireLogRequest{
		HostId:    "h",
		RunId:     "r",
		SessionId: "s",
		Frames:    []*hubv1.WireFrame{f},
		FromSeq:   41,
	}
	if len(push.GetFrames()) != 1 || push.GetFromSeq() != 41 {
		t.Fatalf("push request roundtrip: frames=%d from_seq=%d", len(push.GetFrames()), push.GetFromSeq())
	}
	_ = &hubv1.PushWireLogResponse{}

	// Downlink request + streamed wrapper, exercising the heartbeat kind.
	_ = &hubv1.SubscribeWireLogRequest{HostId: "h", RunId: "r", SessionId: "s", FromSeq: 0}
	_ = &hubv1.SubscribeWireLogResponse{Frame: &hubv1.WireFrame{
		Kind: hubv1.WireFrameKind_WIRE_FRAME_KIND_HEARTBEAT,
	}}

	// Lock the full enum surfaces (zero-value + inbound direction), not just the
	// values used above.
	_ = hubv1.WireDirection_WIRE_DIRECTION_UNSPECIFIED
	_ = hubv1.WireDirection_WIRE_DIRECTION_IN
	_ = hubv1.WireFrameKind_WIRE_FRAME_KIND_UNSPECIFIED

	// Procedure route constants exist for both new RPCs.
	_ = hubv1connect.HubServicePushWireLogProcedure
	_ = hubv1connect.HubServiceSubscribeWireLogProcedure
}

// TestWireLogClientInterface is a compile-time proof the generated client gained
// a unary PushWireLog and a server-stream SubscribeWireLog with the expected
// signatures.
func TestWireLogClientInterface(t *testing.T) {
	var c hubv1connect.HubServiceClient
	if c == nil {
		return
	}
	// Bind returns to explicitly-typed vars so the unary-vs-stream distinction is
	// enforced at compile time: a mismatched RPC cardinality would fail to
	// assign here.
	var (
		pushResp  *connect.Response[hubv1.PushWireLogResponse]
		subStream *connect.ServerStreamForClient[hubv1.SubscribeWireLogResponse]
	)
	pushResp, _ = c.PushWireLog(context.Background(), connect.NewRequest(&hubv1.PushWireLogRequest{}))
	subStream, _ = c.SubscribeWireLog(context.Background(), connect.NewRequest(&hubv1.SubscribeWireLogRequest{}))
	_, _ = pushResp, subStream
}
