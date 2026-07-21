package hub

import (
	"net/url"
	"strings"
)

// EnvHubURL is the environment variable consulted by ResolveHubURL.
const EnvHubURL = "SPRAWL_HUB_URL"

// ResolveHubURL resolves the hub endpoint from three sources in precedence
// order (highest first): an explicit --hub-url flag, the SPRAWL_HUB_URL
// environment variable, then the .sprawl/config.yaml value.
//
// The default is FIRMLY EMPTY: with nothing configured the hub client is inert
// and no endpoint is dialed. There is intentionally no baked-in default hub
// endpoint (public-repo hygiene — docs 01 §3). Whitespace-only candidates are
// treated as empty.
func ResolveHubURL(flag string, getenv func(string) string, configVal string) string {
	if v := strings.TrimSpace(flag); v != "" {
		return v
	}
	if getenv != nil {
		if v := strings.TrimSpace(getenv(EnvHubURL)); v != "" {
			return v
		}
	}
	return strings.TrimSpace(configVal)
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
