package merge

// QUM-1105 scenario tests: the squash commit must carry the AGENT'S OWN
// commit message, not a status blurb.
//
// Why real git rather than mock Deps. The claim is about what a reader of
// `main` can still see after the agent branch is deleted at retire — a
// property of the object store, not of a call sequence. A mock can prove the
// engine passed some string to GitCommit; only a real repo can prove that
// string survived `git commit`'s cleanup and is readable from the parent
// branch afterwards. Both shapes exist here; neither substitutes for the
// other.
//
// Sentinel discipline: every assertion below names a sentinel that exists in
// exactly one source. BLURB-SENTINEL is only ever reachable via
// AgentState.LastReportMessage, so an assertion that finds it has found the
// defect and nothing else. Note the assertions are phrased against the
// intended outcome ("the subject is the commit's subject"), never against the
// defect's symptom ("the message is short") — the latter stops being
// observable the moment the fix lands, which is the QUM-1104 hazard.
//
// DESIGN DECISION these tests pin: LastReportMessage is carried into the
// squash message NOWHERE, not even as labelled context. The issue permits
// additive context, but the field's whole problem is that it is a ≤160-char
// TUI ping under no obligation to be current at merge time — the live case
// named a SHA three amends old. Copying a known-stale field into the one
// durable record is a smaller version of the defect, and "absent" is a
// checkable claim where "present but labelled" needs a reader to notice the
// label. It also keeps the assertion non-vacuous: a test that only inspects
// lines containing the blurb asserts nothing at all once the blurb is gone.
//
// SECOND DESIGN DECISION, pinned by S14: when the derivation range is empty
// the merge FAILS rather than falling back to anything. The tempting
// alternative — use the merge commit's own subject when --no-merges empties
// the range — is rejected for the same reason as the blurb: a merge commit's
// subject is `Merge branch 'x'`, machine-generated boilerplate that describes
// the topology rather than the work, so accepting it reinstates "a message
// arrived, therefore the record was preserved" with a different filler. An
// error is not a dead end: MessageOverride remains the highest-precedence
// source, so a caller who genuinely wants to merge such a branch says what it
// is for. That escape hatch is what makes failing the safe default, and it is
// why S14 asserts the error NAMES the remedy.
//
// Source commit SHAs are recorded in FULL, not abbreviated. `git rev-parse
// --short` length is repo-size dependent, so an abbreviated record would
// couple these assertions to `core.abbrev` in a way that holds in a fixture
// repo and drifts in a real one.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	blurbSentinel = "BLURB-SENTINEL"
	subjSentinel  = "SUBJ-SENTINEL"
)

// commitFileMsg is commitFile with an explicit (possibly multi-line) commit
// message.
//
// The message goes in on STDIN, not as `-m <msg>`. The fixture hit the same
// wall the production code does: building S13a's oversize commit with `-m`
// died with `fork/exec /usr/bin/git: argument list too long`, which is the
// MAX_ARG_STRLEN limit demonstrating itself on this host. `--cleanup=verbatim`
// keeps the fixture's bytes exactly as written, so a later assertion about
// what survived the MERGE cannot be confounded by what git did at SETUP.
func (r *scenarioRepo) commitFileMsg(dir, label, file, content, message string) string {
	r.t.Helper()
	full := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		r.t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		r.t.Fatalf("write: %v", err)
	}
	r.git(dir, "add", "--", file)
	r.gitStdin(dir, message, "commit", "-q", "--cleanup=verbatim", "-F", "-")
	sha := r.git(dir, "rev-parse", "HEAD")
	r.commits[label] = sha
	return sha
}

// gitStdin runs a git command that must succeed, feeding stdin from a string.
func (r *scenarioRepo) gitStdin(dir, stdin string, args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@x",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@x",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// gitStdinOut runs a git command that must succeed, feeding stdin from a
// string, and returns its stdout.
func (r *scenarioRepo) gitStdinOut(dir, stdin string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		r.t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return string(out)
}

// messageOf returns the full raw commit message of ref.
func (r *scenarioRepo) messageOf(ref string) string {
	return r.git(r.root, "log", "-1", "--format=%B", ref)
}

// cfgWithBlurb builds a scenario Config whose LastReportMessage is the blurb
// sentinel — the thing the engine must NOT use as the commit message.
func (r *scenarioRepo) cfgWithBlurb(agentName, agentBranch, agentWT string) *Config {
	cfg := r.scenarioCfg(agentName, agentBranch, agentWT, "true")
	cfg.AgentState.LastReportMessage = blurbSentinel
	return cfg
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// mustContain fails with the full message body, since every failure here is
// "the message says something other than what I expected" and the body is the
// evidence.
func mustContain(t *testing.T, msg, want, why string) {
	t.Helper()
	if !strings.Contains(msg, want) {
		t.Errorf("%s: squash message does not contain %q\n--- message ---\n%s\n---", why, want, msg)
	}
}

// TestMergeCommitMessage_S8_SingleCommitMultiParagraphBodySurvives is the
// central QUM-1105 assertion: a single agent commit with a multi-paragraph
// body and a trailer must arrive on the parent branch intact.
func TestMergeCommitMessage_S8_SingleCommitMultiParagraphBodySurvives(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/m8")
	body := subjSentinel + "\n\nBODY-PARA-1-SENTINEL\n\nBODY-PARA-2-SENTINEL\n\nRefs: QUM-1105"
	r.commitFileMsg(wt, "m8", "m8.txt", "x\n", body)

	if _, err := Merge(context.Background(), r.cfgWithBlurb("m8", "agent/m8", wt), scenarioDeps()); err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	msg := r.messageOf("main")
	if got := firstLine(msg); got != subjSentinel {
		t.Errorf("subject of the squash is %q, want the agent commit's subject %q\n--- message ---\n%s\n---", got, subjSentinel, msg)
	}
	mustContain(t, msg, "BODY-PARA-1-SENTINEL", "first body paragraph")
	mustContain(t, msg, "BODY-PARA-2-SENTINEL", "second body paragraph")
	mustContain(t, msg, "Refs: QUM-1105", "trailer")
	// The Co-Authored-By trailer predates this change and is still part of
	// the contract; pin that derivation does not drop it.
	mustContain(t, msg, "Co-Authored-By: Claude", "Co-Authored-By trailer")

	// Containment is not enough: a trailer glued onto the previous paragraph
	// is still "contained" but is no longer a trailer. Ask git, which is what
	// every consumer of trailers actually uses. The first cut of this change
	// produced exactly that — one newline before the footer instead of two —
	// and every containment assertion above stayed green.
	// Containment is not enough, and this is the finding that produced this
	// assertion. git parses ONLY the last paragraph as trailers, so a
	// free-text provenance footer appended after the body demotes every
	// trailer the agent wrote out of the trailer block: `Refs: QUM-1105`
	// stays present as TEXT and stops being readable by
	// `git interpret-trailers`, by GitHub's co-author attribution, and by
	// anything else that parses them. The first cut of this change did
	// exactly that, and every containment assertion above stayed green — the
	// QUM-1105 shape one level down, preserved to the eye and silently
	// degraded to every machine. So ask git, which is what consumers use.
	parsed := r.gitStdinOut(r.root, msg, "interpret-trailers", "--parse")
	for _, want := range []string{"Refs: QUM-1105", "Co-Authored-By:"} {
		if !strings.Contains(parsed, want) {
			t.Errorf("git does not PARSE %q as a trailer (wrong paragraph?)\n--- parsed ---\n%s\n--- message ---\n%s\n---", want, parsed, msg)
		}
	}
}

// TestMergeCommitMessage_S8b_ExactlyOneCoAuthoredBy — the co-author trailer
// is appended only when the agent's message has none. The guard has to be
// case-insensitive and prefix-keyed, because an agent following CLAUDE.md
// writes `Co-Authored-By: Claude Opus 5 <...>`, which does not contain the
// literal line this package appends: an exact-match guard therefore never
// fires for a convention-following commit and stamps a SECOND, conflicting
// co-author onto every one of them.
func TestMergeCommitMessage_S8b_ExactlyOneCoAuthoredBy(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"agent used our exact trailer", subjSentinel + "\n\nCo-Authored-By: Claude <noreply@anthropic.com>"},
		{"agent used the CLAUDE.md trailer", subjSentinel + "\n\nCo-Authored-By: Claude Opus 5 <noreply@anthropic.com>"},
		{"agent used none", subjSentinel + "\n\nbody"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newScenarioRepo(t)
			wt := r.addWorktree("agent/m8b")
			r.commitFileMsg(wt, "m8b", "m8b.txt", "x\n", tc.body)

			if _, err := Merge(context.Background(), r.cfgWithBlurb("m8b", "agent/m8b", wt), scenarioDeps()); err != nil {
				t.Fatalf("merge failed: %v", err)
			}

			msg := r.messageOf("main")
			if n := strings.Count(strings.ToLower(msg), "co-authored-by:"); n != 1 {
				t.Errorf("got %d Co-Authored-By trailers, want exactly 1\n--- message ---\n%s\n---", n, msg)
			}
		})
	}
}

// TestMergeCommitMessage_S8c_ByteFidelityOfTheAgentsBody — `git commit`'s
// cleanup modes are lossy in ways that matter for the messages this now
// carries: `whitespace` (the default for -F) strips trailing whitespace from
// every line and collapses runs of blank lines. A blank context line in an
// embedded diff is a single space; a markdown hard break is two trailing
// spaces. `--cleanup=verbatim` is what keeps them.
func TestMergeCommitMessage_S8c_ByteFidelityOfTheAgentsBody(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/m8c")
	body := subjSentinel + "\n\nhard break two spaces  \n\n\ntwo blank lines above\n\n\tdiff -u a b\n\t \n\tcontext line above is one space"
	r.commitFileMsg(wt, "m8c", "m8c.txt", "x\n", body)

	if _, err := Merge(context.Background(), r.cfgWithBlurb("m8c", "agent/m8c", wt), scenarioDeps()); err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	if msg := r.messageOf("main"); !strings.Contains(msg, body) {
		t.Errorf("the agent's body was not carried byte-for-byte\n--- want substring ---\n%q\n--- got ---\n%q\n", body, msg)
	}
}

// TestMergeCommitMessage_S9_MultiCommitDerivesFromTheCommits — a multi-commit
// branch must yield a message derived from those commits, including their
// SHAs, which stop existing once the squash lands.
func TestMergeCommitMessage_S9_MultiCommitDerivesFromTheCommits(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/m9")
	var shas []string
	for i := 1; i <= 3; i++ {
		shas = append(shas, r.commitFileMsg(wt, fmt.Sprintf("m9-%d", i), fmt.Sprintf("m9-%d.txt", i), "x\n",
			fmt.Sprintf("SUBJ-%d-SENTINEL\n\nBODY-%d-SENTINEL", i, i)))
	}

	if _, err := Merge(context.Background(), r.cfgWithBlurb("m9", "agent/m9", wt), scenarioDeps()); err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	msg := r.messageOf("main")
	for i := 1; i <= 3; i++ {
		mustContain(t, msg, fmt.Sprintf("SUBJ-%d-SENTINEL", i), fmt.Sprintf("subject of commit %d", i))
		mustContain(t, msg, fmt.Sprintf("BODY-%d-SENTINEL", i), fmt.Sprintf("body of commit %d", i))
	}
	for i, sha := range shas {
		mustContain(t, msg, sha, fmt.Sprintf("SHA of source commit %d", i+1))
	}
}

// TestMergeCommitMessage_S10_ExplicitOverrideStillWins — the override path is
// the one that has been protecting the record; it must keep winning.
//
// This test is expected to PASS before the fix as well as after. That is the
// point: an inventory that goes uniformly red proves only that the tests are
// new, whereas one red-and-green split makes each red attributable.
func TestMergeCommitMessage_S10_ExplicitOverrideStillWins(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/m10")
	r.commitFileMsg(wt, "m10", "m10.txt", "x\n", "COMMIT-SUBJ-SENTINEL\n\nCOMMIT-BODY-SENTINEL")

	cfg := r.cfgWithBlurb("m10", "agent/m10", wt)
	cfg.MessageOverride = "OVERRIDE-SENTINEL: the caller's own subject\n\nOVERRIDE-BODY-SENTINEL"

	if _, err := Merge(context.Background(), cfg, scenarioDeps()); err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	msg := r.messageOf("main")
	if got := firstLine(msg); got != "OVERRIDE-SENTINEL: the caller's own subject" {
		t.Errorf("subject is %q, want the explicit override's subject\n--- message ---\n%s\n---", got, msg)
	}
	mustContain(t, msg, "OVERRIDE-BODY-SENTINEL", "override body")
}

// TestMergeCommitMessage_S11_NeitherStatusFallbackAppears pins the negative
// half of the contract, over BOTH of today's fallbacks: the blurb subject
// (`<name>: <LastReportMessage>`) and, when the blurb is empty, the
// `<name>: merge branch '<branch>'` placeholder. Both legs assert the derived
// subject positively as well, so neither can pass by asserting nothing.
func TestMergeCommitMessage_S11_NeitherStatusFallbackAppears(t *testing.T) {
	for _, tc := range []struct {
		name  string
		blurb string
	}{
		{"blurb set", blurbSentinel},
		{"blurb empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newScenarioRepo(t)
			wt := r.addWorktree("agent/m11")
			r.commitFileMsg(wt, "m11", "m11.txt", "x\n", subjSentinel+"\n\nBODY-SENTINEL")

			cfg := r.scenarioCfg("m11", "agent/m11", wt, "true")
			cfg.AgentState.LastReportMessage = tc.blurb

			if _, err := Merge(context.Background(), cfg, scenarioDeps()); err != nil {
				t.Fatalf("merge failed: %v", err)
			}

			msg := r.messageOf("main")
			if got := firstLine(msg); got != subjSentinel {
				t.Errorf("subject is %q, want the agent commit's subject %q\n--- message ---\n%s\n---", got, subjSentinel, msg)
			}
			if tc.blurb != "" && strings.Contains(msg, tc.blurb) {
				t.Errorf("the status blurb reached the durable commit message\n--- message ---\n%s\n---", msg)
			}
			if strings.Contains(msg, "merge branch 'agent/m11'") {
				t.Errorf("the no-report placeholder subject reached the commit message\n--- message ---\n%s\n---", msg)
			}
		})
	}
}

// TestMergeCommitMessage_S12_ForeignCommitsAreExcluded — an agent that merges
// another branch into its own must not drag that branch's commit messages
// into its squash.
//
// The sibling topology is load-bearing and was arrived at by correction. The
// obvious fixture — the agent merges the PARENT branch in — cannot detect a
// missing --first-parent at all: merge-base(main, agent) is then the foreign
// commit itself, so mergeBase..agentHead excludes it by construction and the
// assertion passes against any implementation. A sibling branch that never
// landed on main is not an ancestor of the merge base, so it appears in the
// plain range and only --first-parent removes it. The merge commit's own
// subject covers --no-merges. Both are asserted, and the precondition check
// below fails the test if a future topology drift makes either vacuous again.
func TestMergeCommitMessage_S12_ForeignCommitsAreExcluded(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/m12")
	sibling := r.addWorktree("agent/sibling")
	r.commitFileMsg(sibling, "foreign", "foreign.txt", "f\n", "FOREIGN-SUBJ-SENTINEL\n\nFOREIGN-BODY-SENTINEL")
	r.git(wt, "merge", "-q", "--no-ff", "-m", "MERGE-COMMIT-SENTINEL", "agent/sibling")
	r.commitFileMsg(wt, "m12", "m12.txt", "x\n", subjSentinel+"\n\nOWN-BODY-SENTINEL")

	// Precondition: the foreign commit and the merge commit must both be IN
	// the plain range, or the exclusion assertions below assert nothing.
	base := r.git(r.root, "merge-base", "main", "agent/m12")
	plainRange := r.git(r.root, "log", "--format=%s", base+"..agent/m12")
	for _, want := range []string{"FOREIGN-SUBJ-SENTINEL", "MERGE-COMMIT-SENTINEL"} {
		if !strings.Contains(plainRange, want) {
			t.Fatalf("fixture is vacuous: %q is not in the plain range %s..agent/m12:\n%s", want, base[:8], plainRange)
		}
	}

	if _, err := Merge(context.Background(), r.cfgWithBlurb("m12", "agent/m12", wt), scenarioDeps()); err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	msg := r.messageOf("main")
	mustContain(t, msg, "OWN-BODY-SENTINEL", "the agent's own commit body")
	for _, unwanted := range []string{"FOREIGN-SUBJ-SENTINEL", "FOREIGN-BODY-SENTINEL", "MERGE-COMMIT-SENTINEL"} {
		if strings.Contains(msg, unwanted) {
			t.Errorf("squash message contains %q, which came from a merged-in foreign branch\n--- message ---\n%s\n---", unwanted, msg)
		}
	}
}

// TestMergeCommitMessage_S13a_OversizeSingleBodySurvives forces the argv
// limit. Linux caps a SINGLE argument at MAX_ARG_STRLEN (128 KiB) regardless
// of ARG_MAX, so `git commit -m <msg>` cannot carry this message at all.
//
// A single commit is what makes this a real gate: a multi-commit fixture can
// be satisfied by summarising or truncating the series, so it would pass with
// `-m` intact and prove nothing. There is no legitimate way to bound ONE
// commit's own message and still claim the record was preserved.
func TestMergeCommitMessage_S13a_OversizeSingleBodySurvives(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/m13a")
	const line = "padding line to push this body past the per-argument limit\n"
	body := subjSentinel + "\n\n" + strings.Repeat(line, 3000) + "\nTAIL-SENTINEL"
	if len(body) < 128*1024 {
		t.Fatalf("fixture is vacuous: body is %d bytes, want > 128 KiB", len(body))
	}
	r.commitFileMsg(wt, "m13a", "m13a.txt", "x\n", body)

	if _, err := Merge(context.Background(), r.cfgWithBlurb("m13a", "agent/m13a", wt), scenarioDeps()); err != nil {
		t.Fatalf("merge failed on an oversize commit message: %v", err)
	}

	msg := r.messageOf("main")
	if got := firstLine(msg); got != subjSentinel {
		t.Errorf("subject is %q, want %q", got, subjSentinel)
	}
	// The tail is the part a size-truncating implementation would lose.
	if !strings.Contains(msg, "TAIL-SENTINEL") {
		t.Errorf("the end of the agent's own commit body did not survive (message is %d bytes)", len(msg))
	}
	// Subject + tail alone would still admit an implementation that elides
	// the MIDDLE. There is no legitimate way to bound one commit's own
	// message and still claim the record was preserved, so require the whole
	// of it to be there.
	if len(msg) < len(body) {
		t.Errorf("squash message is %d bytes but the source body was %d: content was elided", len(msg), len(body))
	}
}

// TestMergeCommitMessage_S13b_LongSeriesKeepsACompleteSHAIndex — however the
// bodies of a long series are bounded, the index of source SHAs must be
// complete: after the squash those commits are reachable only from the
// premerge recovery ref, and the SHA is how a reader relates the two.
func TestMergeCommitMessage_S13b_LongSeriesKeepsACompleteSHAIndex(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/m13b")
	filler := strings.Repeat("padding line to inflate the commit body\n", 100) // ~4 KiB
	var shas []string
	for i := 0; i < 40; i++ {
		shas = append(shas, r.commitFileMsg(wt, fmt.Sprintf("m13b-%d", i), fmt.Sprintf("m13b-%d.txt", i), "x\n",
			fmt.Sprintf("SUBJ-BIG-%02d-SENTINEL\n\n%s", i, filler)))
	}

	if _, err := Merge(context.Background(), r.cfgWithBlurb("m13b", "agent/m13b", wt), scenarioDeps()); err != nil {
		t.Fatalf("merge failed on a large composed message: %v", err)
	}

	// Aggregated into one failure: 40 individual dumps of a >128 KiB message
	// would bury the result.
	msg := r.messageOf("main")
	var missing []string
	for i, sha := range shas {
		if !strings.Contains(msg, sha) {
			missing = append(missing, fmt.Sprintf("#%d sha %s", i, sha[:8]))
		}
		// Subjects too: a bare SHA index would satisfy the loop above while
		// telling a reader nothing about what the series did. Unlike bodies,
		// 40 subjects are ~1 KB, so no size argument justifies dropping them.
		if subj := fmt.Sprintf("SUBJ-BIG-%02d-SENTINEL", i); !strings.Contains(msg, subj) {
			missing = append(missing, "#"+fmt.Sprint(i)+" subject")
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d entries absent from the squash message for a %d-commit series: %s",
			len(missing), len(shas), strings.Join(missing, ", "))
	}
}

// TestMergeCommitMessage_S14_UnderivableRangeFailsBeforeAnyMutation — a
// branch whose only first-parent commit is a merge commit yields an empty
// derivation under --first-parent --no-merges, yet mergeBase != agentHead so
// the no-op early-return does not fire and the branch carries real new
// content. This is exactly the branch a silent fallback would slip back in
// on, so it must fail loudly and, since derivation runs before the first
// mutation, leave the repository untouched.
//
// Note BOTH flags are required for the range to be empty here: --no-merges
// alone still yields the sibling's own commit. Building this fixture from a
// one-flag reading of that sentence produces a merge that succeeds, which is
// the same vacuity that had to be corrected out of S12.
func TestMergeCommitMessage_S14_UnderivableRangeFailsBeforeAnyMutation(t *testing.T) {
	r := newScenarioRepo(t)
	wt := r.addWorktree("agent/m14")
	sibling := r.addWorktree("agent/sibling14")
	r.commitFileMsg(sibling, "sib14", "sib14.txt", "s\n", "SIBLING-SUBJ-SENTINEL")
	r.git(wt, "merge", "-q", "--no-ff", "-m", "MERGE-ONLY-SENTINEL", "agent/sibling14")

	mainBefore, agentBefore := r.sha("main"), r.sha("agent/m14")

	_, err := Merge(context.Background(), r.cfgWithBlurb("m14", "agent/m14", wt), scenarioDeps())

	if err == nil {
		t.Fatalf("merge succeeded on an underivable range; message on main is:\n%s", r.messageOf("main"))
	}
	// Not "does the error mention a message" — that matches `git commit:
	// ... message` and any number of unrelated failures. Because this path
	// refuses the merge outright, the error is the only thing between the
	// caller and a dead end, so assert it names the remedy (CLAUDE.md's
	// next-action-hint rule). The remedy is a named parameter, not a guessed
	// phrasing.
	for _, want := range []string{"--message", "message:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name the %s escape hatch, leaving the caller stuck: %v", want, err)
		}
	}
	if got := r.sha("main"); got != mainBefore {
		t.Errorf("main moved from %s to %s despite the abort", mainBefore[:8], got[:8])
	}
	if got := r.sha("agent/m14"); got != agentBefore {
		t.Errorf("the agent branch moved from %s to %s despite the abort", agentBefore[:8], got[:8])
	}
	if refs := r.premergeRefs(); len(refs) != 0 {
		t.Errorf("premerge refs were written despite aborting before the first mutation: %v", refs)
	}
}
