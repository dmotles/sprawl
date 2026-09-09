package uiapi

import "strings"

// ProjectName reduces a project's `remote_url` to a short display label.
//
// `projects` has exactly three columns — id, remote_url, created_at — so there
// is no name to read and the label has to be derived. That derivation happens
// HERE, server-side, rather than in the browser, for one reason: a remote URL
// is the only field in this schema that routinely carries an employer's
// internal hostnames, org names and sometimes embedded credentials, and a
// derivation done client-side requires shipping the raw URL to the browser to
// do it. Deriving it here means `remote_url` never enters a JSON response at
// all (see Project, which has no field for it).
//
// The LAST path segment only, deliberately — not `org/repo`. The org component
// is exactly the part most likely to name an employer, and it buys nothing a
// reader needs. Two projects can therefore share a label; that is acceptable
// because every response carrying a name also carries `project_id`, which is
// the identity. The name is a label for humans, never a key.
// The forms handled are the ones git actually emits: `scheme://[user[:pass]@]host[:port]/path`,
// scp-style `[user@]host:path`, and a bare local filesystem path. The authority
// is located and DISCARDED rather than parsed, because nothing downstream wants
// any part of it — this is not a URL parser, it is a host stripper that happens
// to keep the last path segment.
//
// net/url is deliberately not used: it rejects the scp-style form outright
// (`git@github.com:org/repo.git` parses as scheme "git@github.com" with no
// host), so a URL-parser-shaped implementation silently falls through to a
// no-op on the single most common remote shape there is.
func ProjectName(remoteURL string) string {
	s := strings.TrimSpace(remoteURL)

	// An authority is present only when something says so. Getting this wrong
	// in the lenient direction turns a local path's first directory into a
	// "host" and drops it.
	hadAuthority := false
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		hadAuthority = true
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		// LastIndex, not Index: a password may itself contain '@'.
		s = s[i+1:]
		hadAuthority = true
	}
	if !hadAuthority {
		// scp-style with no user (`host:org/repo`). A colon BEFORE any slash
		// is the only thing distinguishing it from a local path.
		if c := strings.IndexByte(s, ':'); c >= 0 {
			if slash := strings.IndexByte(s, '/'); slash < 0 || c < slash {
				hadAuthority = true
			}
		}
	}
	if hadAuthority {
		// Drop `host[:port]` — everything up to the first '/' or ':'.
		cut := len(s)
		if i := strings.IndexAny(s, "/:"); i >= 0 {
			cut = i
		}
		s = strings.TrimLeft(s[cut:], "/:")
	}

	s = strings.Trim(s, "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSuffix(s, ".git")
}
