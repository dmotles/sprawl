package hub

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
	"github.com/dmotles/sprawl/internal/hub/gen/hub/v1/hubv1connect"
	"github.com/dmotles/sprawl/internal/hub/store"
	"github.com/dmotles/sprawl/internal/hub/token"
)

func hourFromNow() time.Time { return time.Now().Add(time.Hour) }

// findToken returns the token with the given id from a HostToken slice, or nil.
func findToken(toks []*hubv1.HostToken, id string) *hubv1.HostToken {
	for _, tk := range toks {
		if tk.GetTokenId() == id {
			return tk
		}
	}
	return nil
}

// createViaCookie mints a token through the CreateHostToken RPC authenticated by
// a fresh live cookie, returning the response message.
func createViaCookie(
	t *testing.T,
	client hubv1connect.HubServiceClient,
	mintCookie func(t *testing.T, expires time.Time) (value, id string),
	label string,
) *hubv1.CreateHostTokenResponse {
	t.Helper()
	cookie, _ := mintCookie(t, hourFromNow())
	resp, err := client.CreateHostToken(context.Background(),
		withCookie(connect.NewRequest(&hubv1.CreateHostTokenRequest{Label: label}), cookie))
	if err != nil {
		t.Fatalf("CreateHostToken via cookie: %v", err)
	}
	return resp.Msg
}

// --- Auth matrix: cookie allowed ---

func TestCreateHostToken_CookieAllowed(t *testing.T) {
	client, _, mintCookie, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()

	msg := createViaCookie(t, client, mintCookie, "laptop")
	if msg.GetToken() == "" {
		t.Error("CreateHostToken returned an empty plaintext token")
	}
	if msg.GetTokenId() == "" {
		t.Error("CreateHostToken returned an empty token id")
	}
}

func TestListHostTokens_CookieAllowed(t *testing.T) {
	client, _, mintCookie, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()

	cookie, _ := mintCookie(t, hourFromNow())
	resp, err := client.ListHostTokens(context.Background(),
		withCookie(connect.NewRequest(&hubv1.ListHostTokensRequest{}), cookie))
	if err != nil {
		t.Fatalf("ListHostTokens via cookie: %v", err)
	}
	// The harness seeds one bearer token under MVPUserID, so the list is non-empty.
	if len(resp.Msg.GetTokens()) == 0 {
		t.Error("ListHostTokens returned no tokens; expected the seeded token")
	}
}

func TestRevokeHostToken_CookieAllowed(t *testing.T) {
	client, _, mintCookie, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()

	created := createViaCookie(t, client, mintCookie, "to-revoke")
	cookie, _ := mintCookie(t, hourFromNow())
	if _, err := client.RevokeHostToken(context.Background(),
		withCookie(connect.NewRequest(&hubv1.RevokeHostTokenRequest{TokenId: created.GetTokenId()}), cookie)); err != nil {
		t.Fatalf("RevokeHostToken via cookie: %v", err)
	}
}

// --- Auth matrix: bearer denied ---

func TestCreateHostToken_BearerDenied(t *testing.T) {
	client, bearer, _, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	_, err := client.CreateHostToken(context.Background(),
		withBearer(connect.NewRequest(&hubv1.CreateHostTokenRequest{Label: "x"}), bearer))
	assertUnauthErr(t, err, "CreateHostToken with bearer")
}

func TestListHostTokens_BearerDenied(t *testing.T) {
	client, bearer, _, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	_, err := client.ListHostTokens(context.Background(),
		withBearer(connect.NewRequest(&hubv1.ListHostTokensRequest{}), bearer))
	assertUnauthErr(t, err, "ListHostTokens with bearer")
}

func TestRevokeHostToken_BearerDenied(t *testing.T) {
	client, bearer, _, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	_, err := client.RevokeHostToken(context.Background(),
		withBearer(connect.NewRequest(&hubv1.RevokeHostTokenRequest{TokenId: "whatever"}), bearer))
	assertUnauthErr(t, err, "RevokeHostToken with bearer")
}

// --- Auth matrix: unauthenticated denied ---

func TestCreateHostToken_Unauthenticated(t *testing.T) {
	client, _, _, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	_, err := client.CreateHostToken(context.Background(),
		connect.NewRequest(&hubv1.CreateHostTokenRequest{Label: "x"}))
	assertUnauthErr(t, err, "CreateHostToken unauth")
}

func TestListHostTokens_Unauthenticated(t *testing.T) {
	client, _, _, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	_, err := client.ListHostTokens(context.Background(),
		connect.NewRequest(&hubv1.ListHostTokensRequest{}))
	assertUnauthErr(t, err, "ListHostTokens unauth")
}

func TestRevokeHostToken_Unauthenticated(t *testing.T) {
	client, _, _, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	_, err := client.RevokeHostToken(context.Background(),
		connect.NewRequest(&hubv1.RevokeHostTokenRequest{TokenId: "whatever"}))
	assertUnauthErr(t, err, "RevokeHostToken unauth")
}

// --- Round-trip + crypto ---

func TestHostToken_CreateListRevokeRoundTrip(t *testing.T) {
	client, _, mintCookie, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	ctx := context.Background()

	created := createViaCookie(t, client, mintCookie, "roundtrip")
	id := created.GetTokenId()

	// List shows the token, active (revoked==0), with a real created timestamp.
	cookie, _ := mintCookie(t, hourFromNow())
	listed, err := client.ListHostTokens(ctx,
		withCookie(connect.NewRequest(&hubv1.ListHostTokensRequest{}), cookie))
	if err != nil {
		t.Fatalf("ListHostTokens: %v", err)
	}
	tk := findToken(listed.Msg.GetTokens(), id)
	if tk == nil {
		t.Fatalf("created token %q not in list %+v", id, listed.Msg.GetTokens())
	}
	if tk.GetLabel() != "roundtrip" {
		t.Errorf("label = %q, want roundtrip", tk.GetLabel())
	}
	if tk.GetCreatedAtUnixMs() <= 0 {
		t.Errorf("created_at_unix_ms = %d, want > 0", tk.GetCreatedAtUnixMs())
	}
	if tk.GetRevokedAtUnixMs() != 0 {
		t.Errorf("revoked_at_unix_ms = %d, want 0 (active)", tk.GetRevokedAtUnixMs())
	}

	// Revoke it.
	cookie2, _ := mintCookie(t, hourFromNow())
	if _, err := client.RevokeHostToken(ctx,
		withCookie(connect.NewRequest(&hubv1.RevokeHostTokenRequest{TokenId: id}), cookie2)); err != nil {
		t.Fatalf("RevokeHostToken: %v", err)
	}

	// List again: same token now shows revoked.
	cookie3, _ := mintCookie(t, hourFromNow())
	listed2, err := client.ListHostTokens(ctx,
		withCookie(connect.NewRequest(&hubv1.ListHostTokensRequest{}), cookie3))
	if err != nil {
		t.Fatalf("ListHostTokens (post-revoke): %v", err)
	}
	tk2 := findToken(listed2.Msg.GetTokens(), id)
	if tk2 == nil {
		t.Fatalf("token %q missing after revoke", id)
	}
	if tk2.GetRevokedAtUnixMs() <= 0 {
		t.Errorf("revoked_at_unix_ms = %d, want > 0 after revoke", tk2.GetRevokedAtUnixMs())
	}
}

func TestCreateHostToken_PlaintextParsesAndMatchesID(t *testing.T) {
	client, _, mintCookie, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()

	msg := createViaCookie(t, client, mintCookie, "parse")
	id, _, err := token.Parse(msg.GetToken())
	if err != nil {
		t.Fatalf("returned plaintext does not parse: %v", err)
	}
	if id != msg.GetTokenId() {
		t.Errorf("parsed token id %q != response token_id %q", id, msg.GetTokenId())
	}
}

// The RPC must seal the token under the same pepper the auth path verifies
// against, so a token minted via CreateHostToken authenticates as a bearer.
func TestCreateHostToken_VerifiesAgainstSealingPepper(t *testing.T) {
	client, _, mintCookie, st, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	ctx := context.Background()

	msg := createViaCookie(t, client, mintCookie, "pepper")
	id, secret, err := token.Parse(msg.GetToken())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	recs, err := st.ListTokens(ctx, MVPUserID)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	var rec *store.TokenRecord
	for i := range recs {
		if string(recs[i].TokenID) == id {
			rec = &recs[i]
			break
		}
	}
	if rec == nil {
		t.Fatalf("minted token %q not persisted", id)
	}
	ok, err := token.VerifySealed(ctx, st.Secrets(), rec.Hash, secret)
	if err != nil || !ok {
		t.Fatalf("sealed hash does not verify under hub pepper: ok=%v err=%v", ok, err)
	}
}

// --- Error-code edges ---

func TestRevokeHostToken_UnknownID(t *testing.T) {
	client, _, mintCookie, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	unknown, err := token.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	cookie, _ := mintCookie(t, hourFromNow())
	_, err = client.RevokeHostToken(context.Background(),
		withCookie(connect.NewRequest(&hubv1.RevokeHostTokenRequest{TokenId: unknown.TokenID}), cookie))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound for unknown token id", connect.CodeOf(err))
	}
}

func TestRevokeHostToken_EmptyID(t *testing.T) {
	client, _, mintCookie, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	cookie, _ := mintCookie(t, hourFromNow())
	_, err := client.RevokeHostToken(context.Background(),
		withCookie(connect.NewRequest(&hubv1.RevokeHostTokenRequest{TokenId: ""}), cookie))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument for empty token id", connect.CodeOf(err))
	}
}

func assertUnauthErr(t *testing.T, err error, ctx string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an error, got nil", ctx)
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("%s: code = %v, want Unauthenticated", ctx, connect.CodeOf(err))
	}
}
