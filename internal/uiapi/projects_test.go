package uiapi

import (
	"strings"
	"testing"
)

// The shortening cases. Each `in` is a remote URL shape git actually produces.
func TestProjectName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"scp-style ssh", "git@github.com:dmotles/sprawl.git", "sprawl"},
		{"https with .git", "https://github.com/dmotles/sprawl.git", "sprawl"},
		{"https without .git", "https://github.com/dmotles/sprawl", "sprawl"},
		{"ssh url form", "ssh://git@github.com:22/dmotles/sprawl.git", "sprawl"},
		{"local path", "/home/coder/sprawl", "sprawl"},
		{"trailing slash", "https://github.com/dmotles/sprawl/", "sprawl"},
		{"bare name", "sprawl", "sprawl"},
		{"empty", "", ""},
		// A URL that is nothing but a host must not render the host as the
		// label — there is no repo name in it, so the honest answer is no
		// label, which the UI renders as an em dash.
		{"host only", "https://github.com/", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProjectName(tc.in); got != tc.want {
				t.Errorf("ProjectName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The leak assertion, and the reason this function exists at all (tower,
// QUM-1349): a label must never carry the host, the org, the scheme or an
// embedded credential. This is separate from the table above because it is a
// different claim — the table says the label is RIGHT, this says the label is
// SAFE, and a shortening bug could satisfy either one alone.
func TestProjectName_LeaksNoUrlComponent(t *testing.T) {
	const (
		host   = "git.internal.example.com"
		org    = "some-employer-org"
		user   = "svc-account"
		secret = "ghp-do-not-leak-this"
	)
	in := "https://" + user + ":" + secret + "@" + host + "/" + org + "/widget.git"

	got := ProjectName(in)

	if got != "widget" {
		t.Fatalf("ProjectName(%q) = %q, want %q", in, got, "widget")
	}
	for _, forbidden := range []string{host, org, user, secret, "https", "@", "/"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("label %q contains %q — a remote URL component reached the UI", got, forbidden)
		}
	}
}
