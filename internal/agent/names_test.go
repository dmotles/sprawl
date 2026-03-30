package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAllocateName_ReturnsFirstAvailable(t *testing.T) {
	dir := t.TempDir()
	name, err := AllocateName(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != NamePool[0] {
		t.Errorf("got %q, want %q", name, NamePool[0])
	}
}

func TestAllocateName_SkipsUsedNames(t *testing.T) {
	dir := t.TempDir()
	// Mark first 3 names as used
	for _, name := range NamePool[:3] {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	name, err := AllocateName(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != NamePool[3] {
		t.Errorf("got %q, want %q", name, NamePool[3])
	}
}

func TestAllocateName_AllExhausted(t *testing.T) {
	dir := t.TempDir()
	for _, name := range NamePool {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	_, err := AllocateName(dir)
	if err == nil {
		t.Fatal("expected error when all names exhausted, got nil")
	}
}

func TestNamePoolNoDuplicates(t *testing.T) {
	seen := make(map[string]bool)
	for _, name := range NamePool {
		if seen[name] {
			t.Errorf("duplicate name in pool: %q", name)
		}
		seen[name] = true
	}
}
