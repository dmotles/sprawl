package card

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Byte-exact goldens for the slim rendered prompt (QUM-1318).
//
// The slim cards are what every agent actually spawns with, and the only other
// checks on them are prose marker probes in slim_test.go. Those assert the
// presence and ordering of NAMED markers, so an accidental edit anywhere in a
// slim card that leaves those markers alone changes every agent's system prompt
// with nothing turning red. These goldens are that missing pin.
//
// REGENERATING: after an INTENTIONAL slim-card edit, run
//
//	go test ./internal/card -run TestSlimCards_RenderByteIdenticalToGoldens -update-slim-goldens
//
// and review the resulting testdata diff — it is the real prompt delta. That
// run FAILS by design when it rewrote anything (see below), so a regeneration
// can never be mistaken for a passing suite, and a stale golden can never be
// laundered into green by tacking the flag onto a normal test run.
var updateSlimGoldens = flag.Bool("update-slim-goldens", false,
	"rewrite internal/card/testdata slim goldens from the current cards, then fail so the diff gets reviewed")

// slimGoldenInput is the fixed Input every slim golden is rendered from.
//
// No clock and no ambient environment: every field is a literal, so the bytes
// depend on the card body and Render alone. The env leaves Subagent and TestMode
// off — the banner and sandbox arms are Go constants in render.go, already pinned
// byte-exactly by internal/agent's variant goldens, and this file exists to pin
// the card PROSE that nothing else pins.
func slimGoldenInput() Input {
	return Input{
		AgentName:  "zone",
		ParentName: "tower",
		BranchName: "dmotles/feature-x",
		Family:     "engineering",
		Env: Env{
			WorkDir:  "/work/sprawl",
			Platform: "linux",
			Shell:    "/bin/zsh",
		},
	}
}

// slimGoldenCards returns the slim cards to pin, derived from the embedded seeds
// rather than listed, so a newly embedded slim card fails on a missing golden
// instead of being quietly uncovered.
func slimGoldenCards(t *testing.T) []*Card {
	t.Helper()
	cards, err := Seeds()
	if err != nil {
		t.Fatalf("Seeds: %v", err)
	}
	var out []*Card
	for _, c := range cards {
		if strings.HasPrefix(c.Name, "slim-") {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		t.Fatal("no slim cards are embedded — this file would pin nothing")
	}
	return out
}

// slimGoldenPath names the golden after the card AND its version, so a version
// bump is a deliberate regeneration rather than a silent re-point of the pin.
func slimGoldenPath(c *Card) string {
	return filepath.Join("testdata", fmt.Sprintf("%s@%d.golden", c.Name, c.Version))
}

func TestSlimCards_RenderByteIdenticalToGoldens(t *testing.T) {
	for _, c := range slimGoldenCards(t) {
		t.Run(c.Name, func(t *testing.T) {
			got, err := c.Render(slimGoldenInput())
			if err != nil {
				t.Fatalf("rendering %s@%d: %v", c.Name, c.Version, err)
			}
			path := slimGoldenPath(c)
			want, readErr := os.ReadFile(path)

			if *updateSlimGoldens {
				if readErr == nil && string(want) == got {
					return
				}
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatalf("creating testdata: %v", err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatalf("writing %s: %v", path, err)
				}
				// Deliberately a failure: -update rewrote a golden, which means
				// the prompt every agent spawns with just changed. Exiting 0
				// here is what would let a stale golden be refreshed and
				// reported as a pass in one step.
				t.Fatalf("%s was rewritten from %s@%d — review the testdata diff, then re-run WITHOUT -update-slim-goldens",
					path, c.Name, c.Version)
			}

			if readErr != nil {
				t.Fatalf("reading %s: %v (regenerate with -update-slim-goldens)", path, readErr)
			}
			if got != string(want) {
				i := firstSlimDiff(got, string(want))
				t.Errorf("%s@%d no longer renders %s byte-identically.\ngot %d bytes, want %d bytes; first diff at byte %d\n got: %q\nwant: %q\nIf the card edit was intentional, regenerate with -update-slim-goldens and review the diff.",
					c.Name, c.Version, path, len(got), len(string(want)), i,
					got[i:min(i+120, len(got))], string(want)[i:min(i+120, len(want))])
			}
		})
	}
}

// firstSlimDiff reports the first differing byte index, or -1 if equal.
func firstSlimDiff(a, b string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) == len(b) {
		return -1
	}
	return n
}

// TestSlimCards_MutatedCardBreaksItsGolden is the positive control that licenses
// the test above.
//
// A byte-comparison is the classic shape that goes green while reading nothing:
// if Render ignored the body, or the golden were compared against itself, the
// comparison would pass without the card being consulted. So one character of
// prose changed in a card MUST break exactly that card's golden — and must leave
// every other card's golden intact, which is the half that proves the goldens are
// not all the same file.
func TestSlimCards_MutatedCardBreaksItsGolden(t *testing.T) {
	// Present in every slim card's opening lines. Asserted before the mutation:
	// a Replace that matched nothing would leave the body untouched, and this
	// control would then be claiming an UNMUTATED card differs from its golden.
	const (
		before = "Sprawl"
		after  = "Sprowl"
	)
	cards := slimGoldenCards(t)
	for _, target := range cards {
		t.Run(target.Name, func(t *testing.T) {
			if !strings.Contains(target.Body, before) {
				t.Fatalf("%s has no %q to mutate — this control mutates nothing and proves nothing", target.Name, before)
			}
			mutated := *target
			mutated.Body = strings.Replace(target.Body, before, after, 1)

			got, err := mutated.Render(slimGoldenInput())
			if err != nil {
				t.Fatalf("rendering the mutated card: %v", err)
			}
			if got == readSlimGolden(t, target) {
				t.Fatalf("a one-character prose edit to %s still rendered byte-identically to %s — the golden is not reading the card",
					target.Name, slimGoldenPath(target))
			}
			t.Logf("control fired: mutated %s diverges from its golden at byte %d", target.Name, firstSlimDiff(got, readSlimGolden(t, target)))

			// The other direction: the mutation must not be able to break a
			// different card's golden, or "exactly that card's golden failed"
			// would be unproven.
			for _, other := range cards {
				if other.Name == target.Name {
					continue
				}
				out, err := other.Render(slimGoldenInput())
				if err != nil {
					t.Fatalf("rendering %s: %v", other.Name, err)
				}
				if out != readSlimGolden(t, other) {
					t.Errorf("mutating %s also broke %s's golden — the goldens are not per-card", target.Name, other.Name)
				}
			}
		})
	}
}

func readSlimGolden(t *testing.T, c *Card) string {
	t.Helper()
	data, err := os.ReadFile(slimGoldenPath(c))
	if err != nil {
		t.Fatalf("reading %s: %v (regenerate with -update-slim-goldens)", slimGoldenPath(c), err)
	}
	return string(data)
}
