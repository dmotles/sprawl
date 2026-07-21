package hub

import "testing"

func TestResolveHubURL_EmptyDefault(t *testing.T) {
	// With no flag, no env, no config value, the resolver must return the
	// empty string — the hub endpoint has NO baked-in default (public-repo
	// hygiene: the host connects only when explicitly told to).
	got := ResolveHubURL("", func(string) string { return "" }, "")
	if got != "" {
		t.Fatalf("empty default: want %q, got %q", "", got)
	}
}

func TestResolveHubURL_Precedence(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	tests := []struct {
		name      string
		flag      string
		env       map[string]string
		configVal string
		want      string
	}{
		{
			name:      "flag beats env and config",
			flag:      "https://flag.example:443",
			env:       map[string]string{"SPRAWL_HUB_URL": "https://env.example:443"},
			configVal: "https://config.example:443",
			want:      "https://flag.example:443",
		},
		{
			name:      "env beats config when flag empty",
			flag:      "",
			env:       map[string]string{"SPRAWL_HUB_URL": "https://env.example:443"},
			configVal: "https://config.example:443",
			want:      "https://env.example:443",
		},
		{
			name:      "config used when flag and env empty",
			flag:      "",
			env:       map[string]string{},
			configVal: "https://config.example:443",
			want:      "https://config.example:443",
		},
		{
			name:      "whitespace-only flag is treated as empty, falls through to env",
			flag:      "   ",
			env:       map[string]string{"SPRAWL_HUB_URL": "https://env.example:443"},
			configVal: "https://config.example:443",
			want:      "https://env.example:443",
		},
		{
			name:      "whitespace-only env is treated as empty, falls through to config",
			flag:      "",
			env:       map[string]string{"SPRAWL_HUB_URL": "  \t "},
			configVal: "https://config.example:443",
			want:      "https://config.example:443",
		},
		{
			name:      "whitespace-only config yields empty",
			flag:      "",
			env:       map[string]string{},
			configVal: "   ",
			want:      "",
		},
		{
			name:      "all empty yields empty",
			flag:      "",
			env:       map[string]string{},
			configVal: "",
			want:      "",
		},
		{
			// The teeth of the empty-default public-repo AC: a stray-whitespace
			// value from ANY source must not resolve to a non-empty endpoint.
			name:      "all whitespace across every source yields empty",
			flag:      "  ",
			env:       map[string]string{"SPRAWL_HUB_URL": "\t"},
			configVal: " \n ",
			want:      "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveHubURL(tc.flag, env(tc.env), tc.configVal)
			if got != tc.want {
				t.Fatalf("ResolveHubURL(%q, env, %q): want %q, got %q", tc.flag, tc.configVal, tc.want, got)
			}
		})
	}
}

func TestRedactHubURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty stays empty", raw: "", want: ""},
		{
			name: "strips userinfo, path, and query token",
			raw:  "https://user:s3cr3t-token@hub.example:8443/rpc?token=abc123",
			want: "https://hub.example:8443",
		},
		{
			name: "keeps scheme and host:port only",
			raw:  "https://hub.example:443/hub.v1.HubService/RegisterInstance",
			want: "https://hub.example:443",
		},
		{
			name: "host without port",
			raw:  "http://hub.example",
			want: "http://hub.example",
		},
		{
			// A schemeless host:port parses without error but yields no host
			// (":443" becomes an opaque). It must hit the sentinel, never emit
			// malformed output like "hub.example://".
			name: "schemeless host:port hits sentinel",
			raw:  "hub.example:443",
			want: "<redacted>",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactHubURL(tc.raw)
			if got != tc.want {
				t.Fatalf("RedactHubURL(%q): want %q, got %q", tc.raw, tc.want, got)
			}
		})
	}
}

func TestRedactHubURL_NeverLeaksSecretsOnParseFailure(t *testing.T) {
	// A value that fails to parse as a URL must NOT be echoed back verbatim —
	// it could contain a token. We emit a fixed sentinel instead.
	raw := "::not a url:: token=supersecret"
	got := RedactHubURL(raw)
	if got == raw {
		t.Fatalf("RedactHubURL echoed the raw unparseable value, risking a secret leak: %q", got)
	}
	if want := "<redacted>"; got != want {
		t.Fatalf("RedactHubURL(unparseable): want %q, got %q", want, got)
	}
}
