package hub

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// h2cProtocols enables HTTP/1.1 plus cleartext HTTP/2 (h2c). A managed L7
// ingress (Envoy) terminates TLS and speaks cleartext HTTP/2 to the container,
// so the server must accept h2c. Mirrors the transport spike.
func h2cProtocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return p
}

// Serve binds cfg.Addr and runs the hub server until ctx is cancelled, then
// gracefully drains. It is the entry point cmd/hubd wires SIGTERM into.
func Serve(ctx context.Context, cfg HubConfig) error {
	ln, err := net.Listen("tcp", cfg.addr())
	if err != nil {
		return err
	}
	return serveOn(ctx, ln, cfg)
}

// serveOn runs the hub server on an already-bound listener. Split out so tests
// can bind an ephemeral port without a listen race.
func serveOn(ctx context.Context, ln net.Listener, cfg HubConfig) error {
	srv := NewServer(cfg)
	log := cfg.logger().With("component", "hubd")
	log.Info("hub listening", "addr", ln.Addr().String())
	return runHTTP(ctx, ln, srv.Handler(), srv.health, cfg.grace(), log)
}

// runHTTP serves handler on ln until ctx is cancelled, marking health ready
// once serving begins. On cancel it performs the graceful-shutdown sequence:
// fail readiness FIRST (so the platform stops routing new traffic), then drain
// in-flight requests within grace, then exit.
func runHTTP(
	ctx context.Context,
	ln net.Listener,
	handler http.Handler,
	health *Health,
	grace time.Duration,
	log *slog.Logger,
) error {
	srv := &http.Server{
		Handler:           handler,
		Protocols:         h2cProtocols(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Mark ready synchronously BEFORE spawning the serve goroutine. If it were
	// set inside the goroutine, a ctx that is already (or near-immediately)
	// cancelled could run the drain's SetReady(false) before the goroutine's
	// SetReady(true), leaving /readyz=200 during drain and violating the
	// "fail readiness first" guarantee. Doing it here means the serve goroutine
	// never touches readiness, so drain's SetReady(false) can never be undone.
	health.SetReady(true)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		// Serve returned on its own (listener error); not a graceful path.
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	// Graceful drain: stop advertising readiness before draining so the platform
	// routes no new traffic while in-flight requests finish.
	health.SetReady(false)
	log.Info("draining", "grace", grace.String())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful drain incomplete; forcing close", "error", err)
		_ = srv.Close()
		return err
	}
	log.Info("drained cleanly")
	return nil
}
