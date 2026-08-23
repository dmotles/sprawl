//go:build doclint

// Package-cmd doc-lint guards, split out of skills_dead_api_test.go by
// QUM-1289 and gated behind the `doclint` build tag.
//
// WHY THESE TWO ARE NOT IN THE DEFAULT TEST BINARY. They are documentation
// linters: goSourceFiles reads EVERY tracked .go file into memory and then
// regex-scans each one per banned symbol. That is pure CPU over the whole tree,
// and the race detector instruments all of it for nothing. Measured cold on an
// 8-core host:
//
//	TestSkillsGoSymbolBanListIsDead    1.63s plain   32.57s under -race  (~20x)
//	TestSkillsDoNotNameDeadGoSymbols   0.42s plain    4.91s under -race  (~12x)
//
// 37.5s of ./cmd's 43.5s, in a package reverse-reachable from ~46 of 51
// packages — so it dominated the dependency closure of nearly every change and
// was the single largest cost in QUM-1289's 60s commit gate. Note the
// multiplier: -race is ~+23% across this suite as a whole, so do not reason
// about these from that average.
//
// THE COVERAGE DID NOT MOVE ANYWHERE ELSE AND WAS NOT REDUCED. `make validate`
// (the merge gate) runs `test-doclint`, which builds with -tags doclint and
// runs both tests. scripts/test-doclint-split-unit.sh asserts that step exists,
// is in VALIDATE_STEPS, and actually EXECUTES both tests by name — because
// moving a test behind a tag nothing runs is deleting coverage while looking
// like an optimisation.
//
// Dropping -race here is safe for a structural reason, not a judgement call:
// this file has no goroutines, no channels, no sync package and no t.Parallel,
// so the race detector has nothing to observe. That gate asserts the absence of
// those primitives, so if concurrency ever appears here the claim fails loudly
// instead of rotting into a comment nobody rechecks.

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// bannedGoSymbols are Go identifiers QUM-1186 deleted. A skill naming one is
// teaching from a command that no longer exists.
//
// Scope note: these are matched anywhere in a skill document, INCLUDING inside
// ```go fences, because a fenced code block is exactly where this rot hid.
//
// `newTestRetireDeps` is deliberately NOT here: it looks dead and is not —
// `internal/agent/retire_test.go` still declares it. The guard below caught that
// on the first run, which is the point of having a guard on the list rather than
// trusting the list.
var bannedGoSymbols = []string{
	"retireCmd",
	"runRetire",
	"resolveRetireDeps",
	"retireCascade",
	"retireForce",
	"messagesDeps",
	"resolveMessagesDeps",
	"defaultMessagesDeps",
}

// goSymbolDeclRE matches a Go declaration of the symbol, so the guard below
// keys on the identifier actually being DEFINED somewhere in the tree rather
// than merely mentioned in a comment recording its deletion.
func goSymbolDeclRE(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^(?:func|var|type|\t)?\s*` + regexp.QuoteMeta(name) + `\b\s*(?:=|:=|\()`)
}

// goSourceFiles returns every tracked .go file's contents, keyed by path.
func goSourceFiles(t *testing.T) map[string]string {
	t.Helper()

	root := repoRootFromTest(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "*.go").Output()
	if err != nil {
		t.Fatalf("git ls-files *.go: %v", err)
	}
	files := map[string]string{}
	for _, p := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		files[p] = string(b)
	}
	if len(files) < 100 {
		t.Fatalf("control failed: found %d tracked .go files, expected at least 100 — the walk is broken, so the ban-list guard below would pass vacuously", len(files))
	}
	return files
}

// TestSkillsGoSymbolBanListIsDead guards the ban list itself: a symbol banned
// here that is declared again in the tree means this test, not the skills, is
// stale. Same contract as TestSkillsBanListMatchesLiveTools.
func TestSkillsGoSymbolBanListIsDead(t *testing.T) {
	if len(bannedGoSymbols) < 5 {
		t.Fatalf("assertion-count floor: expected at least 5 banned symbols, have %d", len(bannedGoSymbols))
	}
	src := goSourceFiles(t)
	for _, name := range bannedGoSymbols {
		re := goSymbolDeclRE(name)
		for path, content := range src {
			if re.MatchString(content) {
				t.Errorf("%q is declared in %s; it is not dead, so remove it from bannedGoSymbols", name, path)
			}
		}
	}
}

// TestSkillsDoNotNameDeadGoSymbols is the primary guard for the symbol class.
func TestSkillsDoNotNameDeadGoSymbols(t *testing.T) {
	for path, content := range skillDocs(t) {
		for i, line := range strings.Split(content, "\n") {
			for _, name := range bannedGoSymbols {
				if bannedRefRE(name).MatchString(line) {
					t.Errorf("%s:%d: names %q, a Go identifier deleted by QUM-1186 — the skill is teaching from code that does not exist", path, i+1, name)
				}
			}
		}
	}
}
