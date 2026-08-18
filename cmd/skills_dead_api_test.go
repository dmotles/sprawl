package cmd

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// These tests pin the skill documents under .claude/skills and .agents/skills
// against the live MCP tool surface. Skills are prose that instructs agents to
// call tools; nothing else keeps that prose true when a tool is removed or its
// arguments change.
//
// Oracle split, for whoever extends this file:
//   - MCP tool assertions -> internal/sprawlmcp/tools.go, read as source so
//     this test need not export baseToolDefinitions (same precedent as
//     internal/sprawlmcp/tool_description_sync_test.go reading
//     ../agent/prompt_mode.go). Known blind spot: build-tag-gated tools
//     registered from tools_inject_on.go are not in this oracle. That is
//     acceptable here because skills only ever document the base surface.
//   - CLI subcommand assertions -> rootCmd.Commands(), available in this package.
//
// Only SKILL.md is scanned. No skill currently ships auxiliary markdown; if one
// does, widen skillDocs rather than assuming this still covers it.

// bannedMCPTools are tool names that were removed from the MCP surface.
// QUM-550 collapsed send_async, send_interrupt, and the message() alias into
// send_message. Every entry is also checked against tools.go, so reinstating a
// tool fails this test rather than leaving a stale ban in place.
//
// Policy note: the ban is unconditional, so a skill cannot narrate the
// migration ("send_async was removed; use send_message") either. Skills tell
// agents what to call; changelogs belong elsewhere.
var bannedMCPTools = []string{"send_async", "send_interrupt", "message("}

// bannedRefRE matches a banned name where a skill would actually be telling an
// agent to call it. Two hazards it has to thread: `message(` must not fire on
// `send_message(` (leading identifier char), and it must not fire on ordinary
// English such as "the commit message(s) must be clear". Skills write tool
// names in backticks, so the bare-`message(` form requires one; the other names
// are distinctive enough to match unadorned.
func bannedRefRE(name string) *regexp.Regexp {
	if name == "message(" {
		return regexp.MustCompile("`" + regexp.QuoteMeta(name))
	}
	return regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name))
}

// toolNameDeclRE matches the tool-name field of an MCP tool definition, so the
// ban-list guard keys on a tool actually being declared rather than merely
// mentioned in some other tool's description prose.
func toolNameDeclRE(name string) *regexp.Regexp {
	return regexp.MustCompile(`"name":\s*"` + regexp.QuoteMeta(strings.TrimSuffix(name, "(")) + `"`)
}

var (
	callArgRE       = regexp.MustCompile(`^\s*([a-z_]+):`)
	stringLiteralRE = regexp.MustCompile(`"[^"]*"`)
)

// maxCallBlockLines bounds the report_status block walk. A block that never
// closes within this many lines is reported as a violation rather than silently
// scanned to EOF, which would attribute every later `key:` line to the block.
const maxCallBlockLines = 40

type skillViolation struct {
	line int
	msg  string
}

// scanSkillDoc reports dead-API references in a single skill document.
// reportStatusProps is the live argument set for report_status; any other
// argument documented in a report_status call block is a violation.
func scanSkillDoc(content string, banned []string, reportStatusProps map[string]bool) []skillViolation {
	var out []skillViolation
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		for _, name := range banned {
			if bannedRefRE(name).MatchString(line) {
				out = append(out, skillViolation{line: i + 1, msg: fmt.Sprintf("references removed MCP tool %q", name)})
			}
		}
		// QUM-1186: with report_status deleted there are no live arguments to
		// compare against, so callers pass a nil prop set and the
		// argument-shape scan below is skipped entirely. Skipped rather than
		// inverted because banning the tool NAME is the correct guard, and
		// that is owed to lane 4 — see the note on liveReportStatusProps.
		if reportStatusProps == nil {
			continue
		}
		if !strings.Contains(line, "report_status(") {
			continue
		}
		// Walk the call block by brace balance rather than a fixed window, so
		// the check neither misses a long block nor bleeds into later prose.
		// Braces inside quoted strings are stripped first: `summary: "{x"` would
		// otherwise run the block to EOF, and `state: "a}b"` would close it a
		// line early and miss a genuine bad argument after it.
		closed := false
		for j, depth := i, 0; j < len(lines) && j-i < maxCallBlockLines; j++ {
			bare := stringLiteralRE.ReplaceAllString(lines[j], "")
			depth += strings.Count(bare, "{") - strings.Count(bare, "}")
			if m := callArgRE.FindStringSubmatch(lines[j]); m != nil && !reportStatusProps[m[1]] {
				out = append(out, skillViolation{
					line: j + 1,
					msg:  fmt.Sprintf("report_status has no %q argument (see internal/sprawlmcp/tools.go)", m[1]),
				})
			}
			if depth <= 0 {
				closed = true
				break
			}
		}
		if !closed {
			out = append(out, skillViolation{
				line: i + 1,
				msg:  fmt.Sprintf("report_status( call block does not close within %d lines; cannot check its arguments", maxCallBlockLines),
			})
		}
	}
	return out
}

func mcpToolsSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "internal", "sprawlmcp", "tools.go"))
	if err != nil {
		t.Fatalf("read internal/sprawlmcp/tools.go: %v", err)
	}
	return string(b)
}

// QUM-1186: liveReportStatusProps was removed here. It extracted
// report_status's declared argument names from tools.go so skill prose could
// be checked for stale ARGUMENTS. The tool is deleted, so there are no live
// arguments to compare against, and callers now pass a nil prop set.
//
// FOLLOW-UP OWED, and deliberately not done in this lane: `delegate` and
// `report_status` should join bannedMCPTools, which would make skill prose
// naming them a hard failure. They are NOT added yet because six files under
// .claude/skills/ still document them, and that tree belongs to lane 4 —
// adding the ban now would turn this suite red on work this lane does not own.
// Flagged to the manager. Until then this file guards the other banned names
// but says nothing about these two: a real, stated gap rather than a silent one.

// skillDocs returns every SKILL.md under both skill trees, keyed by
// repo-relative path. It fails per-root rather than in aggregate, so an emptied
// .claude tree cannot hide behind a populated .agents tree.
func skillDocs(t *testing.T) map[string]string {
	t.Helper()

	docs := make(map[string]string)
	for _, root := range []string{".claude", ".agents"} {
		base := filepath.Join(repoRootFromTest(t), root, "skills")
		n := 0
		for name := range listSkillNames(t, base) {
			path := filepath.Join(base, name, "SKILL.md")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			docs[filepath.Join(root, "skills", name, "SKILL.md")] = string(b)
			n++
		}
		if n == 0 {
			t.Fatalf("scanned 0 skill files under %s/skills; the tree moved or the walk is broken", root)
		}
	}
	return docs
}

// TestSkillsBanListMatchesLiveTools guards the ban list itself: a name here
// that is declared again in tools.go means this test, not the skills, is stale.
func TestSkillsBanListMatchesLiveTools(t *testing.T) {
	src := mcpToolsSource(t)
	for _, name := range bannedMCPTools {
		if toolNameDeclRE(name).MatchString(src) {
			t.Errorf("%q is declared as a live tool in internal/sprawlmcp/tools.go; remove it from bannedMCPTools", name)
		}
	}
}

// TestSkillsMatchLiveMCPSurface is the primary guard: skill prose must not
// teach agents to call tools or arguments that no longer exist.
func TestSkillsMatchLiveMCPSurface(t *testing.T) {
	for path, content := range skillDocs(t) {
		for _, v := range scanSkillDoc(content, bannedMCPTools, nil) {
			t.Errorf("%s:%d: %s", path, v.line, v.msg)
		}
	}
}

// TestSkillsDocumentingMessagingNameSendMessage scopes its positive assertion
// to the docs that actually document the messaging surface, rather than to a
// named file (a pending consolidation may move the section) or to the whole
// tree (which would pass on an unrelated mention elsewhere).
func TestSkillsDocumentingMessagingNameSendMessage(t *testing.T) {
	found := 0
	for path, content := range skillDocs(t) {
		if !strings.Contains(content, "## Messaging Tools") {
			continue
		}
		found++
		if !strings.Contains(content, "send_message") {
			t.Errorf("%s has a Messaging Tools section that never names `send_message`", path)
		}
	}
	if found == 0 {
		t.Error("no skill document has a Messaging Tools section; this assertion has stopped checking anything")
	}
}

func TestScanSkillDoc(t *testing.T) {
	// Fixtures, not the production values: adding a ban-list entry or a live
	// report_status argument must not perturb these expectations.
	props := map[string]bool{"state": true, "summary": true}
	banned := []string{"send_async", "send_interrupt", "message("}

	tests := []struct {
		name    string
		content string
		want    []skillViolation
	}{
		{
			name:    "clean doc",
			content: "Use `send_message` to talk to another agent.\n",
		},
		{
			name:    "send_message call is not mistaken for the removed message() alias",
			content: "call `send_message({to, body})` here\nand send_message(x)\n",
		},
		{
			name:    "removed tool",
			content: "line one\nuse `send_async({to, body})` here\n",
			want:    []skillViolation{{line: 2, msg: `references removed MCP tool "send_async"`}},
		},
		{
			name:    "removed message alias",
			content: "- `message(...)` — deprecated\n",
			want:    []skillViolation{{line: 1, msg: `references removed MCP tool "message("`}},
		},
		{
			name:    "unknown argument in a report_status block",
			content: "report_status({\n  state: \"working\",\n  summary: \"x\",\n  detail: \"y\"\n})\n",
			want:    []skillViolation{{line: 4, msg: `report_status has no "detail" argument (see internal/sprawlmcp/tools.go)`}},
		},
		{
			name:    "unknown argument in a long report_status block is still caught",
			content: "report_status({\n  state: \"working\",\n  summary: \"x\",\n\n\n\n\n\n  notes: \"y\"\n})\n",
			want:    []skillViolation{{line: 9, msg: `report_status has no "notes" argument (see internal/sprawlmcp/tools.go)`}},
		},
		{
			name:    "argument-like prose after the block closes is not attributed to report_status",
			content: "report_status({state: \"working\", summary: \"x\"})\n\nfrontmatter:\n  detail: \"y\"\n",
		},
		{
			name:    "a brace inside a string literal does not run the block to EOF",
			content: "report_status({\n  summary: \"use ${x} and {y\"\n})\n\nteam: QUM\ndetail: leaks\n",
		},
		{
			name:    "a closing brace inside a string literal does not end the block early",
			content: "report_status({\n  state: \"a}b\",\n  detail: \"leak?\"\n})\n",
			want:    []skillViolation{{line: 3, msg: `report_status has no "detail" argument (see internal/sprawlmcp/tools.go)`}},
		},
		{
			// The bound is load-bearing: without it the walk reaches line 51
			// and misattributes an unrelated `detail:` to this block.
			name:    "an unterminated block is reported, and does not swallow far-away prose",
			content: "report_status({\n" + strings.Repeat("  filler\n", 49) + "  detail: unrelated\n",
			want:    []skillViolation{{line: 1, msg: "report_status( call block does not close within 40 lines; cannot check its arguments"}},
		},
		{
			name:    "English prose containing message(s) is not a tool reference",
			content: "the commit message(s) must be clear\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scanSkillDoc(tc.content, banned, props)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d violations %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("violation %d: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestToolNameDeclRE pins that the ban-list oracle keys on a tool being
// declared, not on the name appearing in another tool's description prose.
func TestToolNameDeclRE(t *testing.T) {
	re := toolNameDeclRE("send_async")
	if !re.MatchString(`"name":        "send_async",`) {
		t.Error("did not match a real tool-name declaration")
	}
	if re.MatchString(`"description": "replaces the removed send_async tool",`) {
		t.Error("matched a prose mention inside a description; the guard would misreport it as live")
	}
}

// ---------------------------------------------------------------------------
// Cited-file-path oracle (QUM-1236).
//
// Third entry in the "Oracle split" above:
//   - Cited file paths -> the git index at the repo root. Any `dir/file.ext`
//     whose first segment is a tracked top-level directory, and any bare
//     top-level tracked file (`CLAUDE.md`, `Makefile`), must resolve.
//
// This exists because the MCP and CLI oracles above said nothing about prose
// that cites SOURCE FILES, and that is the gap two mandatory skills rotted
// through: go-cli-best-practices and testing-practices spent a release teaching
// from cmd/retire.go, cmd/spawn.go, cmd/messages.go and cmd/report.go, all
// deleted by QUM-1186, while every test in this file stayed green.
//
// STATED BLIND SPOTS — do not read a green run as more than it is:
//   - Symbols are not checked. `newTestRetireDeps` and `defaultMessagesDeps`
//     are dead names inside live files; nothing here catches them.
//   - Line numbers are not checked. `cmd/merge.go:15` asserts only that the
//     file exists, never that line 15 says what the prose claims.
//   - Bare filenames in a subdirectory (`session.go` in a tree sketch) produce
//     no token: they are unresolvable without a directory. Only TOP-LEVEL bare
//     filenames are covered, because those are unambiguous.
//   - A path written after a `../` sibling-worktree prefix is matched and
//     stat'd against THIS tree, because the leading boundary admits `/` so that
//     `$VAR/`-prefixed citations stay covered. BOTH failure directions are
//     live: a sibling-only file is a false alarm, a same-named local file is a
//     miss. Neither is theoretical — say so rather than assuming one direction.
//   - A doc that legitimately NARRATES a deletion ("`cmd/retire.go` was removed
//     by QUM-1186") is indistinguishable from rot here, and the exception list
//     below cannot tell those apart either. That is the known cost of the
//     mechanism, not an oversight.

// skillPathExtensions are the file extensions a citation must end in. Requiring
// one is what separates a citation from a directory or a prose fragment:
// `internal/...`, `scripts/test-` and `web/src/wire/` all produce no token,
// with no allowlist entry needed.
const skillPathExtensions = `go|sh|md|ya?ml|json|proto|ts|tsx`

// skillPathRuntimePrefixes are path prefixes that describe a LIVE AGENT'S
// WORKSPACE rather than the repo. `.sprawl/config.yaml` is tracked and must
// resolve; `.sprawl/memory/persistent.md` is written at runtime and must not be
// required to. Excluding all of `.sprawl` would have dropped the tracked file
// with the untracked ones.
var skillPathRuntimePrefixes = []string{
	".sprawl/agents/",
	".sprawl/memory/",
	".sprawl/worktrees/",
}

// skillPathCandidates returns the top-level names a citation may start with,
// split into directories and bare files.
//
// DERIVED FROM THE GIT INDEX, not from os.ReadDir. Two things that buys:
// upstream citations fall out by construction (.claude/skills/false-red quotes
// golangci-lint's own `pkg/commands/run.go`, and `pkg` is not tracked here, so
// it is never a candidate); and the assertion set cannot silently widen because
// some agent left a scratch directory at the repo root, which a working-tree
// walk would have folded straight into the regex.
func skillPathCandidates(t *testing.T) (dirs, files []string) {
	t.Helper()

	out, err := exec.Command("git", "-C", repoRootFromTest(t), "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	dirSet, fileSet := map[string]bool{}, map[string]bool{}
	for _, p := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if head, _, ok := strings.Cut(p, "/"); ok {
			dirSet[head] = true
		} else if p != "" {
			fileSet[p] = true
		}
	}
	dirs, files = slices.Sorted(maps.Keys(dirSet)), slices.Sorted(maps.Keys(fileSet))

	// Control: a truncated or empty derivation makes every check below pass by
	// finding nothing, which is indistinguishable from a clean tree.
	for _, must := range []string{"cmd", "internal", "scripts", "docs", ".claude"} {
		if !slices.Contains(dirs, must) {
			t.Fatalf("control failed: %q is not among the derived top-level dirs %v — the derivation is broken, so the path oracle would scan nothing", must, dirs)
		}
	}
	for _, must := range []string{"CLAUDE.md", "Makefile"} {
		if !slices.Contains(files, must) {
			t.Fatalf("control failed: %q is not among the derived top-level files — the derivation is broken", must)
		}
	}
	return dirs, files
}

// skillPathRE builds the citation matcher. Group 1 is the path.
//
// The leading boundary class rejects an alphanumeric, `.` or `-` predecessor so
// `mycmd/x.go`, `v1.internal/x.go` and `my-cmd/x.go` do not match on a suffix,
// while still admitting `/` and `$` so `$CTRL/scripts/lib/e2e-common.sh`
// resolves. It is applied to every line, not only to backticked spans, because
// several of the dead citations this was built for lived in `// cmd/retire.go`
// comments inside ```go fences.
//
// Directories are matched before bare files so `docs/README.md` yields the
// docs/ path rather than the top-level `README.md`.
func skillPathRE(dirs, files []string) *regexp.Regexp {
	quoted := make([]string, 0, len(dirs)+len(files))
	for _, d := range dirs {
		quoted = append(quoted, regexp.QuoteMeta(d)+`(?:/[A-Za-z0-9_.\-]+)*\.(?:`+skillPathExtensions+`)`)
	}
	for _, f := range files {
		quoted = append(quoted, regexp.QuoteMeta(f))
	}
	return regexp.MustCompile(`(?:^|[^A-Za-z0-9_.\-])(` + strings.Join(quoted, "|") + `)`)
}

type skillPathCitation struct {
	line int
	path string
}

// scanSkillPaths extracts every cited path from a skill document. The inner
// loop is what makes it find the SECOND and later citations on a line; without
// it the corpus scan collapses from 122 distinct paths to 74.
func scanSkillPaths(content string, re *regexp.Regexp) []skillPathCitation {
	var out []skillPathCitation
	for i, line := range strings.Split(content, "\n") {
		for pos := 0; pos < len(line); {
			m := re.FindStringSubmatchIndex(line[pos:])
			if m == nil {
				break
			}
			out = append(out, skillPathCitation{line: i + 1, path: line[pos+m[2] : pos+m[3]]})
			pos += m[3]
		}
	}
	return out
}

// skillPathIsDead reports whether a cited path fails to resolve. Factored out
// so the existence leg has a unit control of its own: with it inlined, a
// mutation that made every stat succeed would be invisible to every test here
// the moment the tree went green.
func skillPathIsDead(root, cited string) bool {
	for _, prefix := range skillPathRuntimePrefixes {
		if strings.HasPrefix(cited, prefix) {
			return false
		}
	}
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(cited)))
	return err != nil
}

// skillPathExceptions are cited paths that deliberately do not exist here,
// keyed by the skill document allowed to cite them. Keying by document, rather
// than globally, stops one file's excuse from silently covering a future rot in
// a different one.
//
// Each entry is a CLAIM that the path is not ours, and
// TestSkillPathExceptionsAreDead fails if one ever appears in the tree. That
// guard is deliberately weak and cannot distinguish a legitimate excuse from a
// stale citation — both are "does not exist" — so the real brake is
// maxSkillPathExceptions below plus review. "It is only illustrative" is NOT a
// reason: an illustrative path is indistinguishable from a real one to the
// agent reading it, and that category is exactly what let W2/W3 rot.
var skillPathExceptions = map[string]map[string]string{
	".claude/skills/testing-practices/SKILL.md": {
		"cmd/foo.go":      "metasyntactic placeholder; the sentence reads literally \"a new `cmd/foo.go`\"",
		"cmd/foo_test.go": "same placeholder, sibling half",
	},
	".claude/skills/false-red/SKILL.md": {
		"internal/cache/cache.go": "golangci-lint v2.12.2 UPSTREAM source, quoted with line numbers. It needs an " +
			"entry because `internal` is a top-level dir HERE too, so the candidate set admits it; its siblings " +
			"pkg/commands/run.go and pkg/goanalysis/runners_cache.go need none because `pkg` is not tracked here.",
	},
}

// maxSkillPathExceptions is the mechanical brake on the defeat path. The map is
// the one way to make this oracle green without fixing a document, so its
// growth must show up as a diff a reviewer has to justify rather than as a
// quiet extra line.
const maxSkillPathExceptions = 3

// minSkillPathCitations floors the scan PER DOCUMENT, not in aggregate. A
// global floor is satisfied by 0 == 0 somewhere: e2e-matrix alone contributes
// 72 of the corpus's 122 distinct paths, so a global floor of 100 stayed green
// while go-cli-best-practices OR testing-practices — the two documents this
// oracle exists for — went completely dark. Measured, not supposed.
//
// Only documents with a substantial citation count are floored; the rest cite
// one or two paths and a floor on them would just be noise.
var minSkillPathCitations = map[string]int{
	".claude/skills/e2e-matrix/SKILL.md":             50,
	".claude/skills/testing-practices/SKILL.md":      20,
	".claude/skills/go-cli-best-practices/SKILL.md":  8,
	".claude/skills/sprawl-internals/SKILL.md":       6,
	".claude/skills/e2e-testing-sandboxing/SKILL.md": 6,
}

// TestSkillPathExceptionsAreDead guards the exception list itself, in the same
// spirit as TestSkillsBanListMatchesLiveTools: a path excused here that has
// since come back into the tree means this test, not the skill, is stale.
func TestSkillPathExceptionsAreDead(t *testing.T) {
	n := 0
	for doc, entries := range skillPathExceptions {
		for path, why := range entries {
			n++
			if _, err := os.Stat(filepath.Join(repoRootFromTest(t), filepath.FromSlash(path))); err == nil {
				t.Errorf("%s: %s exists in the tree but is excused in skillPathExceptions (%q); drop the entry", doc, path, why)
			}
		}
	}
	if n > maxSkillPathExceptions {
		t.Errorf("skillPathExceptions has %d entries, ceiling is %d — every entry is a hole in the oracle; fix the document instead, or raise the ceiling deliberately and say why", n, maxSkillPathExceptions)
	}
}

// TestSkillPathIsDead is the control on the EXISTENCE leg, in both directions.
func TestSkillPathIsDead(t *testing.T) {
	root := repoRootFromTest(t)

	for _, live := range []string{"cmd/merge.go", "internal/state/state.go", "CLAUDE.md"} {
		if skillPathIsDead(root, live) {
			t.Errorf("skillPathIsDead(%q) = true, want false — the existence check reports a live file as dead", live)
		}
	}
	for _, dead := range []string{"cmd/retire.go", "internal/nonexistent/zzz.go"} {
		if !skillPathIsDead(root, dead) {
			t.Errorf("skillPathIsDead(%q) = false, want true — the existence check cannot see a missing file, so the oracle is vacuous", dead)
		}
	}
	// Runtime-workspace paths are exempt in BOTH the present and absent cases;
	// without this the exemption could be a no-op nobody noticed.
	if skillPathIsDead(root, ".sprawl/memory/persistent.md") {
		t.Error("skillPathIsDead(.sprawl/memory/persistent.md) = true; runtime workspace paths must be exempt")
	}
	if !skillPathIsDead(root, ".sprawl/nonexistent.yaml") {
		t.Error("control failed: a missing path directly under .sprawl was exempted, so the runtime exemption is matching outside its declared prefixes and excuses the whole tree")
	}
}

// TestSkillsCitedPathsExist is the primary guard: a skill may not teach from a
// file that does not exist.
func TestSkillsCitedPathsExist(t *testing.T) {
	re := skillPathRE(skillPathCandidates(t))
	root := repoRootFromTest(t)

	perDoc := map[string]map[string]bool{}
	for path, content := range skillDocs(t) {
		perDoc[path] = map[string]bool{}
		for _, c := range scanSkillPaths(content, re) {
			perDoc[path][c.path] = true
			if _, excused := skillPathExceptions[path][c.path]; excused {
				continue
			}
			if skillPathIsDead(root, c.path) {
				t.Errorf("%s:%d: cites %s, which does not exist", path, c.line, c.path)
			}
		}
	}

	for doc, min := range minSkillPathCitations {
		got, ok := perDoc[doc]
		if !ok {
			t.Errorf("assertion-count floor: %s was not scanned at all", doc)
			continue
		}
		if len(got) < min {
			t.Errorf("assertion-count floor: %s yielded %d distinct cited paths, expected at least %d — this document stopped being scanned, and a green run over it proves nothing",
				doc, len(got), min)
		}
	}
}

// TestScanSkillPaths is the permanent control on the extractor. The wild
// control — running the oracle against the tree before the QUM-1236 doc fixes,
// which failed on 24 citations across 5 documents — cannot be repeated once the
// tree is clean; this table can. Every case below kills at least one mutant of
// skillPathRE or scanSkillPaths; see the commit message for the mutation run.
func TestScanSkillPaths(t *testing.T) {
	re := skillPathRE(skillPathCandidates(t))

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"dead path is extracted", "See `cmd/retire.go` for the shape.", []string{"cmd/retire.go"}},
		{"live path is extracted", "See `internal/state/state.go`.", []string{"internal/state/state.go"}},
		{"line suffix is trimmed off", "`internal/agent/retire.go:82` errors", []string{"internal/agent/retire.go"}},
		{"line range and md extension", "`docs/README.md:157-181` says", []string{"docs/README.md"}},
		{"upstream pkg/ path is not tracked here", "`pkg/commands/run.go:492` in v2.12.2", nil},
		{"shell var prefix still resolves", "$CTRL/scripts/lib/e2e-common.sh", []string{"scripts/lib/e2e-common.sh"}},
		{"bare filename in a subdir has no directory", "edit session.go and runtime.go", nil},
		{"directory is not a citation", "everything under internal/agentops/ moved", nil},
		{"alnum predecessor does not match", "mycmd/retire.go is elsewhere", nil},
		{"dot predecessor does not match", "v1.internal/state/state.go elsewhere", nil},
		{"hyphen predecessor does not match", "my-cmd/retire.go is elsewhere", nil},
		{"comment inside a code fence still matches", "\t// cmd/retire.go", []string{"cmd/retire.go"}},
		{"scan continues past the first citation on a line", "`cmd/retire.go` and `cmd/spawn.go` both", []string{"cmd/retire.go", "cmd/spawn.go"}},
		{"unknown extension is not a citation", "cmd/retire.txt", nil},
		{"top-level bare file is covered", "as `CLAUDE.md` states, and the `Makefile`", []string{"CLAUDE.md", "Makefile"}},
		{"dot-leading top dir is covered", "see `.claude/skills/false-red/SKILL.md`", []string{".claude/skills/false-red/SKILL.md"}},
		{"sibling-worktree prefix is matched against THIS tree", "../weave/cmd/retire.go", []string{"cmd/retire.go"}},
	}

	if len(cases) < 17 {
		t.Fatalf("assertion-count floor: expected at least 17 extractor cases, have %d", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, c := range scanSkillPaths(tc.in, re) {
				got = append(got, c.path)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("scanSkillPaths(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
