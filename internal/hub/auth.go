package hub

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/dmotles/sprawl/internal/hub/store"
	"github.com/dmotles/sprawl/internal/hub/token"
)

// MVPUserID is the single-user MVP's canonical, non-secret user id (docs 07
// §0 / 04 §3). Every token, host, and instance is stamped with it; multi-user
// is a data change (more than one value), not a schema migration.
const MVPUserID store.UserID = "default"

// errUnauthenticated is the single, uniform rejection returned by the auth
// interceptor for EVERY failure mode (missing/malformed/unknown/revoked/
// wrong-secret). Distinct messages would leak a token-enumeration oracle; the
// argon2id constant-time verify is the real gate.
var errUnauthenticated = connect.NewError(
	connect.CodeUnauthenticated, errors.New("invalid or missing bearer token"))

// errStoreUnavailable is returned (fail-closed) when the server has no usable
// store — a misconfiguration, distinct from a bad credential.
var errStoreUnavailable = connect.NewError(
	connect.CodeUnavailable, errors.New("hub store unavailable"))

// NewAuthInterceptor returns a connect interceptor that authenticates the
// `Authorization: Bearer sprawl_hub_<tokenid>_<secret>` header on every
// HubService call — UNARY and SERVER-STREAM alike: parse → O(n) linear lookup
// by tokenid over the single user's tokens → reject if revoked → argon2id
// constant-time verify of the secret against the sealed hash.
//
// If cookie is non-nil (browser login enabled), cookie-eligible RPCs (see
// cookieEligible) additionally accept a valid browser session cookie as an
// ALTERNATIVE to the bearer header — so a logged-in browser can call
// ListInstances or SubscribeWireLog with only its cookie. Host RPCs
// (RegisterInstance, PushWireLog) stay bearer-only. Token-management RPCs (see
// browserOnly) are the inverse: cookie-ONLY, and a host bearer is rejected for
// them regardless of validity.
//
// Streaming handlers are authenticated too: a plain UnaryInterceptorFunc leaves
// WrapStreamingHandler a no-op, which would let any caller subscribe to the
// wire log unauthenticated. This full Interceptor closes that gap by
// authenticating off the stream's request header before the handler runs.
//
// The client always sees a uniform Unauthenticated on rejection; infra failures
// (store/keeper outage) are logged server-side via log so operators can tell
// them apart. A nil log uses a discard logger.
func NewAuthInterceptor(st store.Store, uid store.UserID, cookie *BrowserAuth, log *slog.Logger) connect.Interceptor {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &authInterceptor{st: st, uid: uid, cookie: cookie, log: log}
}

// authInterceptor implements connect.Interceptor. The unary and streaming paths
// share one authorize decision so bearer/cookie rules can never drift apart.
type authInterceptor struct {
	st     store.Store
	uid    store.UserID
	cookie *BrowserAuth
	log    *slog.Logger
}

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := a.authorize(ctx, req.Spec().Procedure, req.Header()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient is a no-op: this interceptor only guards the server side.
func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := a.authorize(ctx, conn.Spec().Procedure, conn.RequestHeader()); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// authorize applies the bearer-then-cookie decision for a procedure. It returns
// nil to admit the call, errStoreUnavailable when no store is configured (fail
// closed), or the uniform errUnauthenticated otherwise.
func (a *authInterceptor) authorize(ctx context.Context, procedure string, header http.Header) error {
	if a.st == nil {
		// Fail closed: never serve an RPC without a store to auth against.
		a.log.Error("auth: no store configured; rejecting request")
		return errStoreUnavailable
	}
	// Bearer first — host auth path — EXCEPT for browser-only procedures (token
	// administration), where a host bearer must never pass regardless of validity.
	if !browserOnly(procedure) {
		if authenticate(ctx, a.st, a.uid, header.Get("Authorization"), a.log) == nil {
			return nil
		}
	}
	// Cookie path for eligible (logged-in-operator) RPCs when browser login is on.
	if a.cookie != nil && cookieEligible(procedure) {
		if a.cookie.authenticateCookie(ctx, header) == nil {
			return nil
		}
	}
	return errUnauthenticated
}

// authenticate verifies a raw Authorization header value. It returns nil on
// success and errUnauthenticated on any failure. Infrastructure errors (store
// query, keeper decrypt) are logged at warn but still collapse to the uniform
// Unauthenticated on the wire (no oracle).
func authenticate(ctx context.Context, st store.Store, uid store.UserID, authHeader string, log *slog.Logger) error {
	raw, ok := strings.CutPrefix(authHeader, "Bearer ")
	if !ok {
		return errUnauthenticated
	}
	tokenID, secret, err := token.Parse(strings.TrimSpace(raw))
	if err != nil {
		return errUnauthenticated
	}

	// Single-user MVP: ListTokens(uid) returns the whole (small) token set, so
	// an O(n) tokenid scan is adequate. TODO(multi-user): add a GetTokenByID
	// store method when per-user listing stops being O(all tokens).
	tokens, err := st.ListTokens(ctx, uid)
	if err != nil {
		log.Warn("auth: token lookup failed (returning uniform unauthenticated)", "error", err)
		return errUnauthenticated
	}
	var rec *store.TokenRecord
	for i := range tokens {
		if string(tokens[i].TokenID) == tokenID {
			rec = &tokens[i]
			break
		}
	}
	if rec == nil || rec.RevokedAt != nil {
		return errUnauthenticated
	}

	ok, err = token.VerifySealed(ctx, st.Secrets(), rec.Hash, secret)
	if err != nil {
		log.Warn("auth: sealed-hash verify failed — keeper/secret issue? (returning uniform unauthenticated)", "error", err)
		return errUnauthenticated
	}
	if !ok {
		return errUnauthenticated
	}
	return nil
}
