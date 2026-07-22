package hub

import (
	"net/url"
	"strings"
)

// EnvHubURL is the environment variable consulted by ResolveHubURL.
const EnvHubURL = "SPRAWL_HUB_URL"

// EnvHubSecretURL names the gocloud.dev/secrets keeper URL used as the
// per-deploy token pepper. It MUST resolve to a stable keeper across hubd
// restarts, or the hub cannot verify previously minted tokens. It is
// resolved at runtime from the secrets path (e.g. base64key://... in dev, a
// cloud KMS ref in prod) — never compiled in (public-repo hygiene).
const EnvHubSecretURL = "SPRAWL_HUB_SECRET_URL" //nolint:gosec // env var NAME, not a credential value

// EnvHubBlobURL names the gocloud.dev/blob bucket URL backing snapshots and
// attachments (e.g. "azblob://bucket" in a real deploy, "file://..." locally).
// It is resolved at runtime from config, never compiled in. When unset the store
// falls back to an in-memory bucket ("mem://") — see internal/hub/store/pg.go.
const EnvHubBlobURL = "SPRAWL_HUB_BLOB_URL"

// ResolveHubURL resolves the hub endpoint from four sources in precedence
// order (highest first): an explicit --hub-url flag, the SPRAWL_HUB_URL
// environment variable, the user-level config (~/.config/sprawl/config.yaml),
// then the project .sprawl/config.yaml value.
//
// The default is FIRMLY EMPTY: with nothing configured the hub client is inert
// and no endpoint is dialed. There is intentionally no baked-in default hub
// endpoint (public-repo hygiene — docs 01 §3). Whitespace-only candidates are
// treated as empty.
func ResolveHubURL(flag string, getenv func(string) string, userVal, projectVal string) string {
	if v := strings.TrimSpace(flag); v != "" {
		return v
	}
	if getenv != nil {
		if v := strings.TrimSpace(getenv(EnvHubURL)); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(userVal); v != "" {
		return v
	}
	return strings.TrimSpace(projectVal)
}

// RedactHubURL reduces a hub URL to "scheme://host[:port]" for safe logging,
// dropping any userinfo, path, and query string (which may carry tokens).
//
// An empty input returns empty. A value that cannot be parsed as a URL returns
// the fixed sentinel "<redacted>" rather than being echoed back — the raw value
// could contain a secret.
func RedactHubURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<redacted>"
	}
	return u.Scheme + "://" + u.Host
}
