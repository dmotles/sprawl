package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/dmotles/sprawl/internal/protocol"
)

// ControlHandler handles a control request of a specific subtype.
type ControlHandler func(ctx context.Context, requestID string, payload json.RawMessage) error

// Router classifies messages and dispatches control requests.
type Router struct {
	transport Transport

	mu       sync.Mutex
	handlers map[string]ControlHandler
	pending  map[string]chan json.RawMessage
	cancels  map[string]context.CancelFunc

	messagesCh chan *protocol.Message
	wg         sync.WaitGroup
}

// NewRouter creates a new Router backed by the given transport.
func NewRouter(t Transport) *Router {
	return &Router{
		transport:  t,
		handlers:   make(map[string]ControlHandler),
		pending:    make(map[string]chan json.RawMessage),
		cancels:    make(map[string]context.CancelFunc),
		messagesCh: make(chan *protocol.Message, 100),
	}
}

// RegisterHandler registers a handler for a control request subtype.
func (r *Router) RegisterHandler(subtype string, h ControlHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[subtype] = h
}

// ReadLoop reads messages from the transport and dispatches them.
// It blocks until the transport returns an error or the context is cancelled.
// The messages channel is closed when ReadLoop returns.
func (r *Router) ReadLoop(ctx context.Context) {
	defer close(r.messagesCh)
	for {
		msg, err := r.transport.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				break
			}
			break
		}
		if msg == nil {
			continue
		}

		switch msg.Type {
		case "keep_alive":
			// skip
		case "control_request":
			r.handleControlRequest(ctx, msg)
		case "control_response":
			r.handleControlResponse(msg)
		case "control_cancel_request":
			r.handleControlCancelRequest(msg)
		default:
			r.messagesCh <- msg
		}
	}
	r.wg.Wait()
}

func (r *Router) handleControlRequest(ctx context.Context, msg *protocol.Message) {
	// Parse the control request to get request_id and request subtype
	var cr struct {
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(msg.Raw, &cr); err != nil {
		return
	}

	var req struct {
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(cr.Request, &req); err != nil {
		return
	}

	r.mu.Lock()
	handler, ok := r.handlers[req.Subtype]
	r.mu.Unlock()

	if !ok {
		return
	}

	// Create per-request context with cancel
	reqCtx, cancel := context.WithCancel(ctx)

	r.mu.Lock()
	r.cancels[cr.RequestID] = cancel
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.cancels, cr.RequestID)
			r.mu.Unlock()
			cancel()
			r.wg.Done()
		}()
		_ = handler(reqCtx, cr.RequestID, cr.Request)
	}()
}

func (r *Router) handleControlResponse(msg *protocol.Message) {
	// Parse response to get request_id
	var cr struct {
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if err := json.Unmarshal(msg.Raw, &cr); err != nil {
		return
	}

	r.mu.Lock()
	ch, ok := r.pending[cr.Response.RequestID]
	if ok {
		delete(r.pending, cr.Response.RequestID)
	}
	r.mu.Unlock()

	if ok {
		ch <- msg.Raw
	}
}

func (r *Router) handleControlCancelRequest(msg *protocol.Message) {
	var cr struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(msg.Raw, &cr); err != nil {
		return
	}

	r.mu.Lock()
	cancel, ok := r.cancels[cr.RequestID]
	r.mu.Unlock()

	if ok {
		cancel()
	}
}

// AddPendingControl registers a channel to receive a control response for the given request ID.
func (r *Router) AddPendingControl(requestID string, ch chan json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending[requestID] = ch
}

// SendControlRequest sends a control request and waits for the correlated response.
func (r *Router) SendControlRequest(ctx context.Context, requestID string, payload any) (json.RawMessage, error) {
	responseCh := make(chan json.RawMessage, 1)
	r.AddPendingControl(requestID, responseCh)

	msg := map[string]any{
		"type":       "control_request",
		"request_id": requestID,
		"request":    payload,
	}

	if err := r.transport.Send(ctx, msg); err != nil {
		r.mu.Lock()
		delete(r.pending, requestID)
		r.mu.Unlock()
		return nil, err
	}

	select {
	case resp := <-responseCh:
		return resp, nil
	case <-ctx.Done():
		r.mu.Lock()
		delete(r.pending, requestID)
		r.mu.Unlock()
		return nil, ctx.Err()
	}
}

// MessagesChan returns the channel of non-control messages.
// The channel is closed when ReadLoop returns.
func (r *Router) MessagesChan() <-chan *protocol.Message {
	return r.messagesCh
}
