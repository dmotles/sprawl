package cmd

import (
	"testing"

	"github.com/dmotles/sprawl/internal/hooks"
)

func TestHooks_SubcommandsRegistered(t *testing.T) {
	hc, _, err := rootCmd.Find([]string{"hooks"})
	if err != nil || hc.Name() != "hooks" {
		t.Fatalf("hooks command not registered: %v", err)
	}
	for _, sub := range []string{"install", "uninstall", "verify"} {
		c, _, err := rootCmd.Find([]string{"hooks", sub})
		if err != nil || c.Name() != sub {
			t.Errorf("hooks %s not registered: %v", sub, err)
		}
	}
}

func TestHooks_InstallHasBranchFlag(t *testing.T) {
	if hooksInstallCmd.Flags().Lookup("branch") == nil {
		t.Error("hooks install must expose a --branch flag")
	}
}

func TestSetHookAssets_Injects(t *testing.T) {
	orig := hookAssets
	t.Cleanup(func() { hookAssets = orig })

	SetHookAssets(hooks.Assets{CommitGuard: []byte("c"), RefGuard: []byte("r"), LeakGuard: []byte("l")})
	if string(hookAssets.CommitGuard) != "c" || string(hookAssets.RefGuard) != "r" || string(hookAssets.LeakGuard) != "l" {
		t.Error("SetHookAssets did not inject the assets")
	}
}

func TestHooksVerify_ResolvesEveryDependency(t *testing.T) {
	// A nil function value in VerifyDeps panics at the first call rather than
	// returning an error, so the command would exit 2 (UNKNOWN) on a perfectly
	// healthy repo. Cheap to check, and it cannot be caught by the shell suite
	// without a real repo in the failing shape.
	d := resolveVerifyDeps()
	for name, fn := range map[string]any{
		"HooksPathOrigins": d.HooksPathOrigins, "ResolvedHooksDir": d.ResolvedHooksDir,
		"CommonDir": d.CommonDir, "GitDir": d.GitDir, "TopLevel": d.TopLevel,
		"Getwd": d.Getwd, "Getenv": d.Getenv, "Lstat": d.Lstat, "Stat": d.Stat,
		"Readlink": d.Readlink, "EvalSymlinks": d.EvalSymlinks, "ReadFile": d.ReadFile,
	} {
		if fn == nil {
			t.Errorf("resolveVerifyDeps left %s nil", name)
		}
	}
}

func TestHooksVerify_TakesNoArguments(t *testing.T) {
	// Without this, `sprawl hooks verify --oops` or a stray argument is silently
	// accepted and the caller believes a check ran against something it did not.
	if err := hooksVerifyCmd.Args(hooksVerifyCmd, []string{"extra"}); err == nil {
		t.Error("hooks verify accepted a positional argument; it must reject one")
	}
	if err := hooksVerifyCmd.Args(hooksVerifyCmd, nil); err != nil {
		t.Errorf("hooks verify rejected a valid no-arg invocation: %v", err)
	}
}
