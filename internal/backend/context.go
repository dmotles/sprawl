package backend

import "context"

type callerIdentityKey struct{}

// WithCallerIdentity returns a new context carrying the caller's agent identity.
// Used by backend sessions to propagate a child agent's identity through MCP
// tool bridge calls so the shared supervisor can act on behalf of the correct agent.
func WithCallerIdentity(ctx context.Context, identity string) context.Context {
	return context.WithValue(ctx, callerIdentityKey{}, identity)
}

// CallerIdentity extracts the caller's agent identity from the context.
// Returns "" if no identity was set (e.g., root weave session).
func CallerIdentity(ctx context.Context) string {
	v, _ := ctx.Value(callerIdentityKey{}).(string)
	return v
}
