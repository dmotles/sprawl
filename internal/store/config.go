package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// secretsFile is ~/.config/sprawl/secrets.yaml.
//
// The DSN lives HERE or in SPRAWL_DB_DSN and NOWHERE ELSE. Specifically not in
// .sprawl/config.yaml, which is a TRACKED file in a PUBLIC repo — the one place
// a database credential must never be able to land.
type secretsFile struct {
	DBDSN string `yaml:"db_dsn"`
}

// SecretsFileName is the basename resolved under the user config dir.
const SecretsFileName = "secrets.yaml"

// EnvDSN is the environment variable that outranks the secrets file.
const EnvDSN = "SPRAWL_DB_DSN"

// ResolveDSN returns the event-log DSN, a human-readable description of WHERE it
// came from, and an error.
//
// `source` names the ORIGIN and never contains the DSN: it is what
// `sprawl store doctor` prints, so if it carried the credential then every
// diagnostic, error message and pasted terminal transcript built from it would
// leak database access — into a public tracker, most likely.
//
// An absent DSN is NOT an error. The store is opt-in, so every `sprawl`
// invocation on a machine that never enabled it resolves the DSN; erroring here
// would either shout at users who never asked for the feature or force callers
// to swallow the error, and swallowing is how a real misconfiguration goes
// unnoticed. "Enabled but unconfigured" IS loud, and it belongs at the Ledger,
// which is the layer that knows the feature flag.
func ResolveDSN(getenv func(string) string, userConfigDir func() (string, error)) (string, string, error) {
	if v := strings.TrimSpace(getenv(EnvDSN)); v != "" {
		return v, EnvDSN, nil
	}

	dir, err := userConfigDir()
	if err != nil {
		return "", "", fmt.Errorf("store: locating the user config dir: %w", err)
	}
	path := filepath.Join(dir, "sprawl", SecretsFileName)

	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("store: reading %s: %w", path, err)
	}

	// Refuse a secrets file anyone but the owner can read. On this project's own
	// hosts many agents run under a single uid in sibling worktrees, and this
	// file holds the credential to the authoritative cross-host event log.
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return "", "", &HintError{
			Err:  fmt.Errorf("%w: %s has mode %#o, want 0600", ErrInsecureSecrets, path, perm),
			Hint: "chmod 600 " + path,
		}
	}

	body, err := os.ReadFile(path) //nolint:gosec // G304: path is derived from the user config dir
	if err != nil {
		return "", "", fmt.Errorf("store: reading %s: %w", path, err)
	}
	var sf secretsFile
	if err := yaml.Unmarshal(body, &sf); err != nil {
		// Deliberately an error rather than "treat as unconfigured": a typo in
		// the secrets file would otherwise silently disable the store, which
		// looks identical to never having enabled it.
		return "", "", fmt.Errorf("store: parsing %s: %w", path, err)
	}
	dsn := strings.TrimSpace(sf.DBDSN)
	if dsn == "" {
		return "", "", nil
	}
	return dsn, path, nil
}
