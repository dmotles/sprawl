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
	for _, sub := range []string{"install", "uninstall"} {
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

	SetHookAssets(hooks.Assets{CommitGuard: []byte("c"), RefGuard: []byte("r")})
	if string(hookAssets.CommitGuard) != "c" || string(hookAssets.RefGuard) != "r" {
		t.Error("SetHookAssets did not inject the assets")
	}
}
