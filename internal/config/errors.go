package config

import (
	"fmt"
	"strings"
)

// KeyProblem is one unrecognized key found in config.yaml.
type KeyProblem struct {
	// Key is the offending key as written in the file.
	Key string
	// Line is the 1-based line the KEY appears on — not the line its value
	// starts on, which differs for a nested block or a block sequence. Zero when
	// the problem did not come from a file (a rejected Set).
	Line int
}

// UnknownKeysError reports every unrecognized key in one go.
//
// The rendered text is self-documenting on purpose: it carries each key with its
// line number, a did-you-mean where one is plausible, the explicit remedy for a
// retired key, and the full recognized-key reference. A user hitting this may
// have no AI assistance available to work out what the valid keys are — and the
// file in question holds the two main-protection commit guards.
//
// The ORDER is load-bearing, not cosmetic: headline, then the reference table,
// then the offending keys, then the next step. Reference() wraps to 26 physical
// rows at 80 columns and cobra prints its usage block above the error, so the
// whole message overflows an 80x24 terminal — the most common floor — and
// whatever renders FIRST scrolls off. QA verified with tmux capture-pane at a
// real 80x24 that under the original ordering it was the offending keys that
// disappeared, leaving the user the list of VALID keys with no indication which
// of theirs was wrong. The actionable detail therefore goes last, nearest the
// prompt; the headline stays first only because it is one line and it is what
// cmd/root.go prints as the error's opening sentence. Pinned by
// TestUnknownKeysError_ActionableDetailComesLast — if you reorder this, that
// test is the one telling you why not.
type UnknownKeysError struct {
	// Path is the config file, when the error came from Load. Empty for a
	// rejected Set.
	Path string
	// FromSet marks an error raised by Config.Set — i.e. the KEY ARGUMENT was
	// wrong, not the file. Without this the message reads "\.sprawl/config.yaml:
	// unrecognized key" for `sprawl config set validat foo`, which blames a file
	// that is perfectly fine and sends the user to edit the wrong thing. An empty
	// Path is not a usable discriminator on its own — it is also what a
	// hypothetical in-memory Load would produce.
	FromSet  bool
	Problems []KeyProblem
}

func (e *UnknownKeysError) Error() string {
	var b strings.Builder

	// The headline. One line, so it survives as the error's opening sentence even
	// though the body below it will overflow the screen.
	keys := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		keys = append(keys, fmt.Sprintf("%q", p.Key))
	}
	switch {
	case e.FromSet:
		// The argument is wrong, not the file — so this branch must never name
		// the config file. See TestSet_UnknownKey_BlamesTheArgumentNotTheFile.
		fmt.Fprintf(&b, "unrecognized config key %s\n\n", strings.Join(keys, ", "))
	case len(e.Problems) == 1:
		fmt.Fprintf(&b, "%s: unrecognized key\n\n", e.pathOrDefault())
	default:
		fmt.Fprintf(&b, "%s: %d unrecognized keys\n\n", e.pathOrDefault(), len(e.Problems))
	}

	b.WriteString(Reference())
	b.WriteString("\n")

	// The offending keys, restated below the table so they are on screen at
	// 80x24. The headline named them too, but the headline is what scrolls off.
	//
	// Both branches share this block deliberately. Two shapes here previously
	// diverged: the Set branch listed its keys inline and left the loop to emit
	// nothing, so a multi-key Set would have stacked hints with no key attached
	// to them. Not reachable today (Config.Set builds exactly one KeyProblem),
	// but the plural is one caller away and the loop is where the attribution
	// lives. This block must NOT name the config file — the Set branch renders
	// it too, and the file is not what is wrong there.
	if len(e.Problems) == 1 {
		b.WriteString("unrecognized key:\n\n")
	} else {
		fmt.Fprintf(&b, "%d unrecognized keys:\n\n", len(e.Problems))
	}
	for _, p := range e.Problems {
		indent := "  "
		if p.Line > 0 {
			fmt.Fprintf(&b, "  line %d: %s\n", p.Line, p.Key)
			indent = "           " // align under the key, past "  line N: "
		} else {
			fmt.Fprintf(&b, "  %s\n", p.Key)
		}
		if hint := hintFor(p.Key); hint != "" {
			fmt.Fprintf(&b, "%s%s\n", indent, hint)
		}
	}

	// The actionable next step, which differs by where the bad key came from.
	if e.FromSet {
		b.WriteString("\nre-run with a recognized key; `sprawl config --help` lists them.\n")
	} else {
		fmt.Fprintf(&b, "\nedit %s, or run: sprawl config --help\n", e.pathOrDefault())
	}
	return b.String()
}

// hintFor returns the remediation line for one unrecognized key, or "" when
// nothing useful can be said and the reference table has to speak for itself.
//
// Order matters:
//  1. A retired key gets its migration verbatim. Edit distance is useless here
//     (`liveness` resembles nothing recognized), and this is the one case where
//     the user genuinely cannot infer the fix from the table.
//  2. A key that is the PREFIX of one or more dotted keys gets told the keys are
//     flat. This is the QUM-1078 real-world mistake — writing `worktree:` as a
//     nested block — where a bare edit-distance suggestion would be unhelpful.
//  3. Otherwise the nearest recognized key, when one is close enough.
//
// pathOrDefault names the config file, falling back to the conventional path.
func (e *UnknownKeysError) pathOrDefault() string {
	if e.Path == "" {
		return ".sprawl/config.yaml"
	}
	return e.Path
}

func hintFor(key string) string {
	if remedy, ok := retiredKeys[key]; ok {
		return remedy
	}

	// A YAML merge key. Verified against yaml.v3 v3.0.1: a whole-mapping decode
	// EXPANDS `<<: *anchor`, but Load walks keys individually (so it can name the
	// offending config key in a type error instead of naming a Go type), and that
	// walk sees the literal `<<`. Rejecting it is correct — every key here is a
	// flat scalar, so there is no section to merge — but edit distance says
	// nothing useful about `<<`, so the remedy has to be spelled out.
	if key == "<<" {
		return "YAML merge keys are not supported — write each key out flat on its own line"
	}

	var dotted []string
	for _, k := range KnownKeys() {
		if strings.HasPrefix(k, key+".") {
			dotted = append(dotted, k)
		}
	}
	if len(dotted) > 0 {
		return fmt.Sprintf("%s are flat dotted keys, not a nested block — write `%s: ...` on one line",
			strings.Join(dotted, " and "), dotted[0])
	}

	if s := suggest(key); s != "" {
		return fmt.Sprintf("did you mean %q?", s)
	}
	return ""
}
