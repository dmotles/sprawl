package cmd

import (
	"os"
	"testing"
)

func TestFindDendraBin_EnvVarSet(t *testing.T) {
	t.Setenv("DENDRA_BIN", "/custom/path/dendra")
	path, err := FindDendraBin()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/custom/path/dendra" {
		t.Errorf("FindDendraBin() = %q, want %q", path, "/custom/path/dendra")
	}
}

func TestFindDendraBin_EnvVarUnset_FallsBackToExecutable(t *testing.T) {
	t.Setenv("DENDRA_BIN", "")
	os.Unsetenv("DENDRA_BIN")
	path, err := FindDendraBin()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return os.Executable() result — a non-empty path
	if path == "" {
		t.Error("FindDendraBin() returned empty string, expected os.Executable() result")
	}
}

func TestFindDendraBin_EnvVarEmpty_FallsBackToExecutable(t *testing.T) {
	t.Setenv("DENDRA_BIN", "")
	path, err := FindDendraBin()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path == "" {
		t.Error("FindDendraBin() returned empty string, expected os.Executable() result")
	}
}
