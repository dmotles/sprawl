package hub

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
	"github.com/dmotles/sprawl/internal/hub/store"
	"github.com/dmotles/sprawl/internal/hub/token"
)

// seedToken creates a valid, active token in st and returns its plaintext.
func seedToken(t *testing.T, st store.Store) string {
	t.Helper()
	ctx := context.Background()
	if err := st.EnsureUser(ctx, MVPUserID); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	m, err := token.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	sealed, err := token.SealedHash(ctx, st.Secrets(), m.Secret)
	if err != nil {
		t.Fatalf("SealedHash: %v", err)
	}
	if err := st.CreateToken(ctx, store.TokenRecord{
		TokenID: store.TokenID(m.TokenID),
		UserID:  MVPUserID,
		Hash:    sealed,
		Label:   "test",
	}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	return m.Plaintext
}

func newMemStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// callWithAuth runs the interceptor around a spy next-func and returns whether
// next was reached and the resulting error.
func callWithAuth(t *testing.T, st store.Store, authHeader string) (reached bool, err error) {
	t.Helper()
	interceptor := NewAuthInterceptor(st, MVPUserID, nil, nil)
	next := func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		reached = true
		return connect.NewResponse(&hubv1.RegisterInstanceResponse{}), nil
	}
	req := connect.NewRequest(&hubv1.RegisterInstanceRequest{HostId: "h1"})
	if authHeader != "" {
		req.Header().Set("Authorization", authHeader)
	}
	_, err = interceptor.WrapUnary(next)(context.Background(), req)
	return reached, err
}

func TestAuthInterceptor_ValidTokenPasses(t *testing.T) {
	st := newMemStore(t)
	plaintext := seedToken(t, st)
	reached, err := callWithAuth(t, st, "Bearer "+plaintext)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if !reached {
		t.Fatal("next handler was not reached for a valid token")
	}
}

func assertUnauth(t *testing.T, reached bool, err error) {
	t.Helper()
	if reached {
		t.Fatal("next handler reached despite a rejected token")
	}
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestAuthInterceptor_NilStoreFailsClosed(t *testing.T) {
	interceptor := NewAuthInterceptor(nil, MVPUserID, nil, nil)
	reached := false
	next := func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		reached = true
		return connect.NewResponse(&hubv1.RegisterInstanceResponse{}), nil
	}
	req := connect.NewRequest(&hubv1.RegisterInstanceRequest{HostId: "h1"})
	req.Header().Set("Authorization", "Bearer sprawl_hub_a_b")
	_, err := interceptor.WrapUnary(next)(context.Background(), req)
	if reached {
		t.Fatal("next reached with a nil store — must fail closed")
	}
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("code = %v, want Unavailable", connect.CodeOf(err))
	}
}

func TestAuthInterceptor_MissingHeader(t *testing.T) {
	st := newMemStore(t)
	seedToken(t, st)
	reached, err := callWithAuth(t, st, "")
	assertUnauth(t, reached, err)
}

func TestAuthInterceptor_NoBearerPrefix(t *testing.T) {
	st := newMemStore(t)
	plaintext := seedToken(t, st)
	reached, err := callWithAuth(t, st, plaintext) // token without "Bearer "
	assertUnauth(t, reached, err)
}

func TestAuthInterceptor_EmptyBearerToken(t *testing.T) {
	st := newMemStore(t)
	seedToken(t, st)
	reached, err := callWithAuth(t, st, "Bearer ")
	assertUnauth(t, reached, err)
}

func TestAuthInterceptor_MalformedToken(t *testing.T) {
	st := newMemStore(t)
	seedToken(t, st)
	reached, err := callWithAuth(t, st, "Bearer not-a-valid-token")
	assertUnauth(t, reached, err)
}

func TestAuthInterceptor_UnknownTokenID(t *testing.T) {
	st := newMemStore(t)
	seedToken(t, st)
	// A well-formed but never-created token.
	other, err := token.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	reached, err := callWithAuth(t, st, "Bearer "+other.Plaintext)
	assertUnauth(t, reached, err)
}

func TestAuthInterceptor_RevokedToken(t *testing.T) {
	st := newMemStore(t)
	plaintext := seedToken(t, st)
	tokenID, _, err := token.Parse(plaintext)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := st.RevokeToken(context.Background(), store.TokenID(tokenID)); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	reached, err := callWithAuth(t, st, "Bearer "+plaintext)
	assertUnauth(t, reached, err)
}

func TestAuthInterceptor_WrongSecret(t *testing.T) {
	st := newMemStore(t)
	plaintext := seedToken(t, st)
	tokenID, _, err := token.Parse(plaintext)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Same tokenid, a different (well-formed) secret.
	other, err := token.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	forged := token.Prefix + tokenID + "_" + other.Secret
	reached, err := callWithAuth(t, st, "Bearer "+forged)
	assertUnauth(t, reached, err)
}

func TestAuthInterceptor_UniformErrorMessage(t *testing.T) {
	st := newMemStore(t)
	plaintext := seedToken(t, st)
	tokenID, _, _ := token.Parse(plaintext)
	other, _ := token.Mint()

	_, missing := callWithAuth(t, st, "")
	_, malformed := callWithAuth(t, st, "Bearer bad")
	_, unknown := callWithAuth(t, st, "Bearer "+other.Plaintext)
	_, wrongSecret := callWithAuth(t, st, "Bearer "+token.Prefix+tokenID+"_"+other.Secret)

	msgs := []error{missing, malformed, unknown, wrongSecret}
	want := missing.Error()
	for _, e := range msgs {
		if e == nil {
			t.Fatal("expected non-nil error on a reject path")
		}
		if e.Error() != want {
			t.Errorf("error message %q differs from %q — leaks an enumeration oracle", e.Error(), want)
		}
	}
}
