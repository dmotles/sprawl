package card

import (
	"strings"
	"testing"
)

func testCard(t *testing.T, src string) *Card {
	t.Helper()
	c, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

const tokenCard = `---
name: tok
version: 1
agent_type: engineer
---
agent={{AGENT_NAME}} parent={{PARENT_NAME}} branch={{BRANCH_NAME}} family={{FAMILY}}
`

func TestRender_SubstitutesEveryKnownToken(t *testing.T) {
	got, err := testCard(t, tokenCard).Render(Input{
		AgentName: "zone", ParentName: "root", BranchName: "sprawl/zone", Family: "engineering",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "agent=zone parent=root branch=sprawl/zone family=engineering"
	if got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
}

// TestRender_RefusesAnUnsubstitutedToken pins the direction that matters: a
// prompt carrying a literal "{{...}}" reaches a model and reads as an
// instruction about a placeholder. Failing the spawn is the safe outcome.
//
// The card is constructed directly rather than parsed because Parse already
// refuses unknown tokens — this asserts the render-time backstop, which is what
// catches knownTokens and Render drifting apart.
func TestRender_RefusesAnUnsubstitutedToken(t *testing.T) {
	c := &Card{Name: "tok", Version: 1, AgentType: "engineer", Body: "hi {{FUTURE_TOKEN}}"}
	got, err := c.Render(Input{AgentName: "zone"})
	if err == nil {
		t.Fatalf("Render accepted a body with an unsubstituted token and returned %q", got)
	}
	if !strings.Contains(err.Error(), "FUTURE_TOKEN") {
		t.Errorf("the error does not name the offending token: %v", err)
	}
	if got != "" {
		t.Errorf("Render returned a usable prompt alongside its error: %q", got)
	}
}

func TestRender_EnvContextBlockIsOptedInto(t *testing.T) {
	env := Env{WorkDir: "/w", Platform: "linux", Shell: "/bin/zsh"}
	in := Input{AgentName: "a", BranchName: "b", Env: env}

	off := testCard(t, tokenCard)
	got, err := off.Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, "# Environment") {
		t.Errorf("a card that did not opt in still got an env block:\n%s", got)
	}

	on := testCard(t, tokenCard)
	on.Opts.AppendEnvContext = true
	got, err = on.Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "\n\n# Environment\n- Working directory: /w\n- Git repository: yes\n- Git branch: b\n- Platform: linux\n- Shell: /bin/zsh\n"
	if !strings.HasSuffix(got, want) {
		t.Errorf("env block wrong.\ngot tail: %q\nwant suffix: %q", got[max(0, len(got)-len(want)-20):], want)
	}
}

// TestRender_EnvContextOmitsUnknownFields pins that an unset field is dropped
// rather than rendered as an empty bullet — "- Shell: " tells an agent its
// shell is the empty string.
func TestRender_EnvContextOmitsUnknownFields(t *testing.T) {
	c := testCard(t, tokenCard)
	c.Opts.AppendEnvContext = true
	got, err := c.Render(Input{AgentName: "a", BranchName: "b"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, absent := range []string{"Working directory", "Platform", "Shell"} {
		if strings.Contains(got, absent) {
			t.Errorf("unset env field %q was rendered anyway:\n%s", absent, got)
		}
	}
	if !strings.Contains(got, "- Git branch: b\n") {
		t.Errorf("the branch bullet is unconditional and is missing:\n%s", got)
	}
}

func TestRender_SubagentBannerRequiresBothTheOptionAndTheEnv(t *testing.T) {
	const marker = "# SUB-AGENT BANNER:"
	cases := []struct {
		name       string
		opt, isSub bool
		want       bool
	}{
		{"neither", false, false, false},
		{"option only", true, false, false},
		{"env only", false, true, false},
		{"both", true, true, true},
	}
	for _, tc := range cases {
		c := testCard(t, tokenCard)
		c.Opts.SubagentBanner = tc.opt
		got, err := c.Render(Input{AgentName: "a", Env: Env{Subagent: tc.isSub, ParentName: "weave"}})
		if err != nil {
			t.Fatalf("%s: Render: %v", tc.name, err)
		}
		present := strings.Contains(got, marker)
		if present != tc.want {
			t.Errorf("%s: banner present = %t, want %t", tc.name, present, tc.want)
		}
		if tc.want {
			if !strings.HasPrefix(got, marker) {
				t.Errorf("%s: the banner must lead the prompt, not sit inside it", tc.name)
			}
			if !strings.Contains(got, `parent agent "weave"`) {
				t.Errorf("%s: the banner does not name the parent: %q", tc.name, got[:min(200, len(got))])
			}
		}
	}
}

func TestRender_SandboxWarningRequiresBothTheOptionAndTestMode(t *testing.T) {
	const marker = "# TEST SANDBOX MODE"
	for _, tc := range []struct {
		name          string
		opt, testMode bool
		want          bool
	}{
		{"neither", false, false, false},
		{"option only", true, false, false},
		{"test mode only", false, true, false},
		{"both", true, true, true},
	} {
		c := testCard(t, tokenCard)
		c.Opts.SandboxWarning = tc.opt
		got, err := c.Render(Input{AgentName: "a", Env: Env{TestMode: tc.testMode}})
		if err != nil {
			t.Fatalf("%s: Render: %v", tc.name, err)
		}
		present := strings.Contains(got, marker)
		if present != tc.want {
			t.Errorf("%s: sandbox warning present = %t, want %t", tc.name, present, tc.want)
		}
	}
}

// TestRender_SplicedBlocksAreTextuallyPinned is a golden-in-source pin on the
// two blocks this package duplicates from internal/agent.
//
// Yes, it restates the constants. That is deliberate and it is the only thing
// that survives the migration: today the duplication is held together by
// internal/agent's TestLegacyCards_VariantArmsMatchTheBuilders, which compares
// a rendered card to Build*Prompt — and Build*Prompt is deleted two slices from
// now. Without this, truncating a bullet out of the sandbox warning or losing a
// clause from the sub-agent banner leaves every remaining test green, because
// they only ever assert the heading is PRESENT.
func TestRender_SplicedBlocksAreTextuallyPinned(t *testing.T) {
	const wantBanner = "# SUB-AGENT BANNER:\n" +
		"You are a SPRAWL SUB-AGENT sharing the git worktree and branch of your parent agent \"weave\". " +
		"Do NOT git checkout, git switch, or git branch — your commits land directly on your parent's working branch. " +
		"Coordinate file edits with your parent. You retain your own inbox, MCP session, and state file."

	const wantWarning = "\n\n# TEST SANDBOX MODE\n\n" +
		"You are operating in a testing sandbox for sprawl. Take care to:\n" +
		"- Avoid taking any action outside of $SPRAWL_ROOT\n" +
		"- ONLY execute sprawl using $SPRAWL_BIN (do not use bare 'sprawl' from PATH)\n" +
		"- Do not interact with production systems, push to remote repositories, or modify files outside the test directory\n" +
		"- This environment will be torn down after testing"

	c := testCard(t, tokenCard)
	c.Opts.SubagentBanner = true
	c.Opts.SandboxWarning = true
	got, err := c.Render(Input{
		AgentName: "a",
		Env:       Env{Subagent: true, ParentName: "weave", TestMode: true},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.HasPrefix(got, wantBanner+"\n\n") {
		t.Errorf("sub-agent banner text drifted.\n got: %q\nwant: %q",
			got[:min(len(wantBanner)+40, len(got))], wantBanner)
	}
	if !strings.HasSuffix(got, wantWarning) {
		t.Errorf("test-sandbox warning text drifted.\n got: %q\nwant: %q",
			got[max(0, len(got)-len(wantWarning)-40):], wantWarning)
	}
}

// TestEnvContextBlock_ExactRendering pins the byte-exact output of
// envContextBlock across its GATING branches. It moved here from internal/agent
// in QUM-1251 along with the function: the block is now spliced by Render for
// any card whose render.append_env_context is set, which is the engineer, QA and
// manager cards — NOT the researcher card, which carries no "# Environment"
// section at all (researcher_tui.golden contains zero occurrences of it).
//
// The QUM-1223 provenance below is retained because it records which rows were
// MEASURED to be load-bearing and which are cheap guards; re-deriving that is
// expensive and losing it invites deleting the wrong rows.
//
// QUM-1223 converted its four b.WriteString(fmt.Sprintf(...)) calls to
// fmt.Fprintf(&b, ...) to satisfy staticcheck QF1012.
//
// What this adds over the pre-existing coverage, stated precisely because the
// first version of this comment got it wrong (QUM-1223): the
// TestBuild*Prompt_TuiGolden suite ALREADY compares full builder output
// byte-for-byte against testdata/*.golden, and it does catch a dropped
// newline here — measured: dropping the "\n" after "- Git repository: yes"
// fails TestBuildEngineerPrompt_TuiGolden, TestBuildManagerPrompt_TuiGolden
// and TestBuildQAPrompt_TuiGolden. So newline detection is NOT this test's
// contribution.
//
// The goldens only ever render testEnvConfig(), which sets WorkDir, Platform
// and Shell all non-empty, so the three `if x != ""` gates have no byte-exact
// coverage anywhere in the repo.
//
// Which rows are the unique contribution, measured rather than asserted:
//   - the three SINGLETON rows — coupling two gates
//     (`if env.Platform != "" && env.Shell != ""`) fails only
//     .../platform_only and nothing else in the package;
//   - the EMPTY BRANCH row — gating the branch line on `branchName != ""`
//     fails only .../empty_branch_name.
//
// The other two rows are redundant and kept only to localise a failure to this
// function instead of to a 20KB golden diff: the all-set row duplicates
// engineer_tui.golden, and the all-empty row duplicates the pre-existing
// TestBuild{Engineer,Manager,QA}Prompt_EnvironmentOmitsEmptyFields and this
// package's TestRender_EnvContextOmitsUnknownFields, which
// already catch coarse gate removal (`if x != ""` -> `if true`).
func TestEnvContextBlock_ExactRendering(t *testing.T) {
	tests := []struct {
		name   string
		branch string
		env    Env
		want   string
	}{
		{
			// TestMode/Subagent/ParentName are set here deliberately while
			// want stays free of them: envContextBlock reads none of the
			// three, and this pins that non-dependence for zero extra rows.
			// It fails the day someone adds a TestMode-gated line to the
			// block. (The sandbox warning and the sub-agent banner are the
			// callers' business — see Render.)
			name:   "all optional fields populated",
			branch: "dmotles/qum-1223",
			env: Env{
				WorkDir: "/tmp/worktrees/finn", Platform: "linux", Shell: "/bin/zsh",
				TestMode: true, Subagent: true, ParentName: "weave",
			},
			want: "\n\n# Environment\n" +
				"- Working directory: /tmp/worktrees/finn\n" +
				"- Git repository: yes\n" +
				"- Git branch: dmotles/qum-1223\n" +
				"- Platform: linux\n" +
				"- Shell: /bin/zsh\n",
		},
		{
			// Pins the gating: only the two unconditional lines survive.
			name:   "all optional fields empty",
			branch: "main",
			env:    Env{},
			want: "\n\n# Environment\n" +
				"- Git repository: yes\n" +
				"- Git branch: main\n",
		},
		{
			name:   "work dir only",
			branch: "b",
			env:    Env{WorkDir: "/w"},
			want:   "\n\n# Environment\n- Working directory: /w\n- Git repository: yes\n- Git branch: b\n",
		},
		{
			name:   "platform only",
			branch: "b",
			env:    Env{Platform: "darwin"},
			want:   "\n\n# Environment\n- Git repository: yes\n- Git branch: b\n- Platform: darwin\n",
		},
		{
			name:   "shell only",
			branch: "b",
			env:    Env{Shell: "/bin/bash"},
			want:   "\n\n# Environment\n- Git repository: yes\n- Git branch: b\n- Shell: /bin/bash\n",
		},
		{
			// Pins the rendered contract for values containing '%', which a
			// worktree path or $SHELL can genuinely carry. This case is
			// contract documentation, NOT the detection mechanism: the
			// format-string class it was first written for is already covered
			// by `go vet`, which `go test` runs. Measured (QUM-1223) — the
			// botched conversion
			// fmt.Fprintf(&b, "- Working directory: "+v+"\n") does not
			// compile at all: "non-constant format string in call to
			// fmt.Fprintf". No mutation was found that only this case
			// catches, so it is kept as a cheap guard on a reachable input
			// rather than credited with detection power it does not have.
			name:   "values containing percent verbs",
			branch: "feat/100%-done",
			env:    Env{WorkDir: "/tmp/100%done", Platform: "%s%d", Shell: "/bin/z%vsh"},
			want: "\n\n# Environment\n" +
				"- Working directory: /tmp/100%done\n" +
				"- Git repository: yes\n" +
				"- Git branch: feat/100%-done\n" +
				"- Platform: %s%d\n" +
				"- Shell: /bin/z%vsh\n",
		},
		{
			// The branch name is unconditional, so an empty one still renders
			// the label. Pins that reachable shape; no other row covers it.
			name:   "empty branch name",
			branch: "",
			env:    Env{WorkDir: "/w"},
			want:   "\n\n# Environment\n- Working directory: /w\n- Git repository: yes\n- Git branch: \n",
		},
		{
			// Same status as the percent-verbs row above: no mutation was
			// found that only this case catches. Kept as a cheap guard on
			// reachable inputs, not credited with detection power.
			name:   "values containing newline and non-ascii",
			branch: "br\nanch",
			env:    Env{WorkDir: "/wörk\ndir", Platform: "líñux", Shell: "/bin/zsh\t"},
			want: "\n\n# Environment\n" +
				"- Working directory: /wörk\ndir\n" +
				"- Git repository: yes\n" +
				"- Git branch: br\nanch\n" +
				"- Platform: líñux\n" +
				"- Shell: /bin/zsh\t\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := envContextBlock(tc.branch, tc.env)
			if got != tc.want {
				t.Errorf("envContextBlock rendered the environment block differently than the\nprompt contract requires — this text is appended to every agent's system prompt.\ngot  %q\nwant %q", got, tc.want)
			}
		})
	}
}
