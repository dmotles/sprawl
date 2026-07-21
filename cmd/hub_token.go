package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/dmotles/sprawl/internal/hub"
	"github.com/dmotles/sprawl/internal/hub/store"
	"github.com/dmotles/sprawl/internal/hub/token"
	"github.com/spf13/cobra"
)

// hubTokenDeps injects the store opener + IO so the token admin commands are
// testable without a real database. It mirrors hubMigrateDeps.
type hubTokenDeps struct {
	Getenv    func(string) string
	Stdout    io.Writer
	Stderr    io.Writer
	OpenStore func(ctx context.Context, dsn string) (store.Store, error)
}

func resolveHubTokenDeps() *hubTokenDeps {
	return &hubTokenDeps{
		Getenv: os.Getenv,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		OpenStore: func(ctx context.Context, dsn string) (store.Store, error) {
			// The sealing keeper (per-deploy pepper) MUST be the same one hubd
			// uses, or hubd cannot verify tokens minted here. Both resolve it
			// from SPRAWL_HUB_SECRET_URL (a gocloud secrets keeper URL, e.g.
			// base64key://... or a cloud KMS ref) — never compiled in.
			return store.NewPGStore(ctx, store.PGConfig{
				DSN:       dsn,
				SecretURL: os.Getenv(hub.EnvHubSecretURL),
			})
		},
	}
}

var hubTokenLabel string

func init() {
	hubTokenCmd.PersistentFlags().StringVar(&hubMigrateDSN, "dsn", "",
		"Postgres DSN (or set SPRAWL_HUB_DSN). Injected at runtime; never committed.")
	hubTokenCreateCmd.Flags().StringVar(&hubTokenLabel, "label", "",
		"human-readable label for the token (e.g. \"laptop repo-1\")")
	hubTokenCmd.AddCommand(hubTokenCreateCmd, hubTokenListCmd, hubTokenRevokeCmd)
	hubCmd.AddCommand(hubTokenCmd)
}

var hubTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage host bearer tokens (create/list/revoke)",
	Long: "Host-token administration, run directly against the hub's Postgres " +
		"store (hub-side admin — not via RPC). Tokens are stored hashed; the " +
		"plaintext is shown exactly once at create time and never again.",
}

var hubTokenCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Mint a new host bearer token (plaintext shown ONCE)",
	Long: "Mint a new host bearer token. The full token is printed to stdout " +
		"exactly once — copy it into a 0600 file on the host. Only a hashed " +
		"form is persisted; the hub cannot redisplay it.",
	Args: cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		return runHubTokenCreate(c.Context(), resolveHubTokenDeps(), hubMigrateDSN, hubTokenLabel)
	},
}

var hubTokenListCmd = &cobra.Command{
	Use:   "list",
	Short: "List host tokens (metadata only; never the secret)",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		return runHubTokenList(c.Context(), resolveHubTokenDeps(), hubMigrateDSN)
	},
}

var hubTokenRevokeCmd = &cobra.Command{
	Use:   "revoke <tokenid>",
	Short: "Revoke a host token by its token id",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		return runHubTokenRevoke(c.Context(), resolveHubTokenDeps(), hubMigrateDSN, args[0])
	},
}

func runHubTokenCreate(ctx context.Context, deps *hubTokenDeps, dsnFlag, label string) error {
	dsn, err := resolveHubDSN(deps.Getenv, dsnFlag)
	if err != nil {
		return err
	}
	st, err := deps.OpenStore(ctx, dsn)
	if err != nil {
		return fmt.Errorf("opening hub store: %w", err)
	}
	defer func() { _ = st.Close() }()

	// The singleton user must exist before the tokens FK write.
	if err := st.EnsureUser(ctx, hub.MVPUserID); err != nil {
		return fmt.Errorf("ensuring user: %w", err)
	}

	m, err := token.Mint()
	if err != nil {
		return fmt.Errorf("minting token: %w", err)
	}
	sealed, err := token.SealedHash(ctx, st.Secrets(), m.Secret)
	if err != nil {
		return fmt.Errorf("sealing token hash: %w", err)
	}
	if err := st.CreateToken(ctx, store.TokenRecord{
		TokenID: store.TokenID(m.TokenID),
		UserID:  hub.MVPUserID,
		Hash:    sealed,
		Label:   label,
	}); err != nil {
		return fmt.Errorf("persisting token: %w", err)
	}

	// The plaintext is emitted exactly once, to stdout, and never persisted.
	fmt.Fprintln(deps.Stdout, m.Plaintext)
	fmt.Fprintln(deps.Stderr, "This token is shown ONCE and cannot be recovered. "+
		"Store it in a 0600 file on the host (or SPRAWL_HUB_TOKEN); never on a CLI flag or in logs.")
	fmt.Fprintf(deps.Stderr, "Next: install it on a host and start `sprawl enter` with --hub-url set.\n")
	return nil
}

func runHubTokenList(ctx context.Context, deps *hubTokenDeps, dsnFlag string) error {
	dsn, err := resolveHubDSN(deps.Getenv, dsnFlag)
	if err != nil {
		return err
	}
	st, err := deps.OpenStore(ctx, dsn)
	if err != nil {
		return fmt.Errorf("opening hub store: %w", err)
	}
	defer func() { _ = st.Close() }()

	toks, err := st.ListTokens(ctx, hub.MVPUserID)
	if err != nil {
		return fmt.Errorf("listing tokens: %w", err)
	}
	if len(toks) == 0 {
		fmt.Fprintln(deps.Stderr, "No host tokens. Create one with: sprawl hub token create --label <name>")
		return nil
	}
	tw := tabwriter.NewWriter(deps.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TOKEN_ID\tLABEL\tCREATED\tSTATUS")
	for _, tk := range toks {
		status := "active"
		if tk.RevokedAt != nil {
			status = "revoked (" + tk.RevokedAt.UTC().Format(time.RFC3339) + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			tk.TokenID, tk.Label, tk.CreatedAt.UTC().Format(time.RFC3339), status)
	}
	return tw.Flush()
}

func runHubTokenRevoke(ctx context.Context, deps *hubTokenDeps, dsnFlag, tokenID string) error {
	dsn, err := resolveHubDSN(deps.Getenv, dsnFlag)
	if err != nil {
		return err
	}
	st, err := deps.OpenStore(ctx, dsn)
	if err != nil {
		return fmt.Errorf("opening hub store: %w", err)
	}
	defer func() { _ = st.Close() }()

	if err := st.RevokeToken(ctx, store.TokenID(tokenID)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no token with id %q (list them with: sprawl hub token list)", tokenID)
		}
		return fmt.Errorf("revoking token: %w", err)
	}
	fmt.Fprintf(deps.Stderr, "Revoked token %s. It is rejected on the next host call/reconnect.\n", tokenID)
	return nil
}
