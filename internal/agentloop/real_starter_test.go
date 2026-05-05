package agentloop

import (
	"context"
	"runtime"
	"syscall"
	"testing"

	"github.com/dmotles/sprawl/internal/claude"
)

// TestStart_SetsPdeathsigOnClaudeCmd guards QUM-458 layer 2: the claude
// subprocess MUST be spawned with Pdeathsig=SIGKILL so it dies when sprawl
// enter is SIGKILL'd, and Setpgid=true so cancelFn can pgkill the whole tree.
//
// Red phase: buildClaudeCmd is a stub that does not yet call
// procutil.SetPdeathsig. This test fails until the implementer wires it in.
func TestStart_SetsPdeathsigOnClaudeCmd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Pdeathsig is Linux-only")
	}
	ctx := context.Background()
	config := ProcessConfig{
		ClaudePath: "/bin/true",
		Args: claude.LaunchOpts{
			SessionID: "test-session",
		},
	}
	cmd := buildClaudeCmd(ctx, config)
	if cmd.SysProcAttr == nil {
		t.Fatalf("buildClaudeCmd must set SysProcAttr (QUM-458 Pdeathsig wiring missing)")
	}
	if cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Errorf("Pdeathsig = %v, want SIGKILL", cmd.SysProcAttr.Pdeathsig)
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Errorf("Setpgid = false, want true (required for KillProcessGroup cancelFn)")
	}
}

func TestBuildArgs_IncludesModelOpus(t *testing.T) {
	config := ProcessConfig{
		Args: claude.LaunchOpts{
			Print:          true,
			InputFormat:    "stream-json",
			OutputFormat:   "stream-json",
			Verbose:        true,
			Model:          "opus",
			Effort:         "medium",
			PermissionMode: "bypassPermissions",
			SessionID:      "test-session",
		},
	}

	args := config.Args.BuildArgs()

	// Verify --model opus is present and comes after --verbose.
	verboseIdx := -1
	modelIdx := -1
	for i, arg := range args {
		if arg == "--verbose" {
			verboseIdx = i
		}
		if arg == "--model" && i+1 < len(args) && args[i+1] == "opus" {
			modelIdx = i
		}
	}

	if verboseIdx == -1 {
		t.Fatal("--verbose not found in args")
	}
	if modelIdx == -1 {
		t.Fatal("--model opus not found in args")
	}
	if modelIdx <= verboseIdx {
		t.Errorf("--model (index %d) should come after --verbose (index %d)", modelIdx, verboseIdx)
	}
}

func TestBuildArgs_EffortMediumDefault(t *testing.T) {
	config := ProcessConfig{
		Args: claude.LaunchOpts{
			Effort:    "medium",
			SessionID: "test-session",
		},
	}

	args := config.Args.BuildArgs()

	found := false
	for i, arg := range args {
		if arg == "--effort" && i+1 < len(args) && args[i+1] == "medium" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected --effort medium in args, but not found")
	}
}

func TestBuildArgs_ContainsExpectedFlags(t *testing.T) {
	config := ProcessConfig{
		Args: claude.LaunchOpts{
			Print:          true,
			InputFormat:    "stream-json",
			OutputFormat:   "stream-json",
			Verbose:        true,
			Model:          "opus",
			Effort:         "medium",
			PermissionMode: "bypassPermissions",
			SessionID:      "sess-1",
			SystemPrompt:   "you are helpful",
			Resume:         true,
		},
	}

	args := config.Args.BuildArgs()

	// Note: --session-id and --system-prompt are intentionally NOT expected —
	// Resume=true suppresses them (see internal/claude TestBuildArgs_Resume*).
	expected := map[string]bool{
		"-p": false, "--input-format": false, "--output-format": false,
		"--verbose": false, "--model": false, "--effort": false,
		"--permission-mode": false,
		"--resume":          false,
	}
	for _, arg := range args {
		if _, ok := expected[arg]; ok {
			expected[arg] = true
		}
	}
	for flag, found := range expected {
		if !found {
			t.Errorf("expected flag %q not found in args", flag)
		}
	}
}
