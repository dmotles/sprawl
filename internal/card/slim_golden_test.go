package card

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// slimGoldenVariant is one (env, golden-file) axis every slim card is pinned on.
//
// Two variants rather than one, because the render OPTIONS are as easy to break
// by accident as the prose is. Every slim card sets subagent_banner and
// sandbox_warning, and both arms are inert unless the env asks for them — so with
// a plain env only, flipping either flag to false in a seed would change every
// sub-agent's and every sandbox agent's prompt and break no golden here. The
// legacy variant goldens in internal/agent pin those blocks' TEXT, but only
// through the LEGACY cards, and say so; whether a SLIM card splices them was
// pinned by nothing before this.
type slimGoldenVariant struct {
	name string
	env  Env
}

// slimGoldenVariants is sorted and literal so subtests and filenames are stable.
func slimGoldenVariants() []slimGoldenVariant {
	return []slimGoldenVariant{
		// The shape agents actually spawn in.
		{name: "plain", env: Env{WorkDir: "/work/sprawl", Platform: "linux", Shell: "/bin/zsh"}},
		// Both conditional arms at once: one extra file per card covers both,
		// and neither arm can hide behind the other because the banner is a
		// PREFIX and the warning a SUFFIX.
		{name: "subagent-testmode", env: Env{
			WorkDir: "/work/sprawl", Platform: "linux", Shell: "/bin/zsh",
			Subagent: true, TestMode: true, ParentName: "tower",
		}},
	}
}

// slimGoldenInput is the fixed Input every slim golden is rendered from.
//
// No clock and no ambient environment: every field is a literal, so the bytes
// depend on the card body, the variant's env, and Render alone.
func slimGoldenInput(v slimGoldenVariant) Input {
	return Input{
		AgentName:  "zone",
		ParentName: "tower",
		BranchName: "dmotles/feature-x",
		Family:     "engineering",
		Env:        v.env,
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

// slimGoldenPath names the golden after the card, its VERSION and the variant, so
// a version bump is a deliberate regeneration rather than a silent re-point of
// the pin.
func slimGoldenPath(c *Card, v slimGoldenVariant) string {
	return filepath.Join("testdata", fmt.Sprintf("%s@%d.%s.golden", c.Name, c.Version, v.name))
}

func TestSlimCards_RenderByteIdenticalToGoldens(t *testing.T) {
	for _, c := range slimGoldenCards(t) {
		for _, v := range slimGoldenVariants() {
			t.Run(c.Name+"/"+v.name, func(t *testing.T) {
				got, err := c.Render(slimGoldenInput(v))
				if err != nil {
					t.Fatalf("rendering %s@%d: %v", c.Name, c.Version, err)
				}
				path := slimGoldenPath(c, v)
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
					// Deliberately a failure: -update rewrote a golden, which
					// means the prompt some agent spawns with just changed.
					// Exiting 0 here is what would let a stale golden be
					// refreshed and reported as a pass in one step.
					t.Fatalf("%s was rewritten from %s@%d — review the testdata diff, then re-run WITHOUT -update-slim-goldens",
						path, c.Name, c.Version)
				}

				if readErr != nil {
					t.Fatalf("reading %s: %v (regenerate with -update-slim-goldens)", path, readErr)
				}
				if got != string(want) {
					i := firstSlimDiff(got, string(want))
					t.Errorf("%s@%d no longer renders %s byte-identically.\ngot %d bytes, want %d bytes; first diff at byte %d\n got: %q\nwant: %q\nIf the card edit was intentional, regenerate with -update-slim-goldens and review the diff.",
						c.Name, c.Version, path, len(got), len(want), i,
						got[i:min(i+120, len(got))], string(want)[i:min(i+120, len(want))])
				}
			})
		}
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
// prose changed in a card MUST break that card's golden, in every variant.
func TestSlimCards_MutatedCardBreaksItsGolden(t *testing.T) {
	// Present in every slim card's opening lines. Asserted before the mutation:
	// a Replace that matched nothing would leave the body untouched, and this
	// control would then be claiming an UNMUTATED card differs from its golden.
	const (
		before = "Sprawl"
		after  = "Sprowl"
	)
	for _, target := range slimGoldenCards(t) {
		if !strings.Contains(target.Body, before) {
			t.Errorf("%s has no %q to mutate — this control mutates nothing and proves nothing", target.Name, before)
			continue
		}
		mutated := *target
		mutated.Body = strings.Replace(target.Body, before, after, 1)

		for _, v := range slimGoldenVariants() {
			t.Run(target.Name+"/"+v.name, func(t *testing.T) {
				got, err := mutated.Render(slimGoldenInput(v))
				if err != nil {
					t.Fatalf("rendering the mutated card: %v", err)
				}
				want := readSlimGolden(t, target, v)
				if got == want {
					t.Fatalf("a one-character prose edit to %s still rendered byte-identically to %s — the golden is not reading the card",
						target.Name, slimGoldenPath(target, v))
				}
				t.Logf("control fired: mutated %s diverges from its golden at byte %d", target.Name, firstSlimDiff(got, want))
			})
		}
	}
}

// TestSlimCards_GoldensAreDistinctPerCardAndVariant is the other half of the
// control above: that a card's golden failed says nothing about "exactly that
// card's golden" unless the goldens differ from each other in the first place.
//
// Compared by CONTENT rather than by path, because paths are distinct by
// construction (slimGoldenPath interpolates the name and variant) and so a
// path-level check could not fail. Two identical files would mean a mutation
// reaching one card is indistinguishable from one reaching another.
func TestSlimCards_GoldensAreDistinctPerCardAndVariant(t *testing.T) {
	seen := map[string]string{}
	var keys []string
	for _, c := range slimGoldenCards(t) {
		for _, v := range slimGoldenVariants() {
			body := readSlimGolden(t, c, v)
			if prev, dup := seen[body]; dup {
				t.Errorf("%s and %s are byte-identical — a break in one is indistinguishable from a break in the other",
					prev, slimGoldenPath(c, v))
			}
			seen[body] = slimGoldenPath(c, v)
			keys = append(keys, slimGoldenPath(c, v))
		}
	}
	sort.Strings(keys)
	if len(keys) < 2 {
		t.Fatalf("only %d golden(s) — distinctness is not a claim about anything", len(keys))
	}
}

func readSlimGolden(t *testing.T, c *Card, v slimGoldenVariant) string {
	t.Helper()
	path := slimGoldenPath(c, v)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (regenerate with -update-slim-goldens)", path, err)
	}
	return string(data)
}
