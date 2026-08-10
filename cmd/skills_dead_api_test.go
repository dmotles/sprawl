package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
