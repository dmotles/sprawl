package cmd

import (
	"os"
	"testing"
)

func TestFindSprawlBin_EnvVarSet(t *testing.T) {
	t.Setenv("SPRAWL_BIN", "/custom/path/dendra")
	path, err := FindSprawlBin()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/custom/path/dendra" {
		t.Errorf("FindSprawlBin() = %q, want %q", path, "/custom/path/dendra")
	}
}

func TestFindSprawlBin_EnvVarUnset_FallsBackToExecutable(t *testing.T) {
	t.Setenv("SPRAWL_BIN", "")
	os.Unsetenv("SPRAWL_BIN")
	path, err := FindSprawlBin()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return os.Executable() result — a non-empty path
	if path == "" {
		t.Error("FindSprawlBin() returned empty string, expected os.Executable() result")
	}
}

func TestFindSprawlBin_EnvVarEmpty_FallsBackToExecutable(t *testing.T) {
	t.Setenv("SPRAWL_BIN", "")
	path, err := FindSprawlBin()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path == "" {
		t.Error("FindSprawlBin() returned empty string, expected os.Executable() result")
	}
}
