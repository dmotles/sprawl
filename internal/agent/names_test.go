package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllocateName_EngineerReturnsTreeName(t *testing.T) {
	dir := t.TempDir()
	name, err := AllocateName(dir, "engineer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != EngineerNames[0] {
		t.Errorf("got %q, want %q", name, EngineerNames[0])
	}
}

func TestAllocateName_ResearcherReturnsRiverName(t *testing.T) {
	dir := t.TempDir()
	name, err := AllocateName(dir, "researcher")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != ResearcherNames[0] {
		t.Errorf("got %q, want %q", name, ResearcherNames[0])
	}
}

func TestAllocateName_ManagerReturnsMountainName(t *testing.T) {
	dir := t.TempDir()
	name, err := AllocateName(dir, "manager")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != ManagerNames[0] {
		t.Errorf("got %q, want %q", name, ManagerNames[0])
	}
}

func TestAllocateName_TesterReturnsFromEngineerPool(t *testing.T) {
	dir := t.TempDir()
	name, err := AllocateName(dir, "tester")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != EngineerNames[0] {
		t.Errorf("got %q, want %q (tester should use engineer pool)", name, EngineerNames[0])
	}
}

func TestAllocateName_SkipsUsedNames(t *testing.T) {
	dir := t.TempDir()
	// Mark first 3 engineer names as used
	for _, name := range EngineerNames[:3] {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	name, err := AllocateName(dir, "engineer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != EngineerNames[3] {
		t.Errorf("got %q, want %q", name, EngineerNames[3])
	}
}

func TestAllocateName_UnknownTypeReturnsError(t *testing.T) {
	dir := t.TempDir()
	_, err := AllocateName(dir, "unknown")
	if err == nil {
		t.Fatal("expected error for unknown agent type, got nil")
	}
	if !strings.Contains(err.Error(), "unknown agent type") {
		t.Errorf("error should mention unknown agent type, got: %v", err)
	}
}

func TestAllocateName_ExhaustedPoolFallsBackToNumericSuffix(t *testing.T) {
	dir := t.TempDir()
	// Fill all researcher names
	for _, name := range ResearcherNames {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	name, err := AllocateName(dir, "researcher")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "river-1" {
		t.Errorf("got %q, want %q", name, "river-1")
	}
}

func TestAllocateName_FallbackSkipsUsedNumericNames(t *testing.T) {
	dir := t.TempDir()
	// Fill all manager names
	for _, name := range ManagerNames {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Also mark peak-1 as used
	if err := os.WriteFile(filepath.Join(dir, "peak-1.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	name, err := AllocateName(dir, "manager")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "peak-2" {
		t.Errorf("got %q, want %q", name, "peak-2")
	}
}

func TestAllocateName_EngineerFallbackUsesTreePrefix(t *testing.T) {
	dir := t.TempDir()
	// Fill all engineer names
	for _, name := range EngineerNames {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	name, err := AllocateName(dir, "engineer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "tree-1" {
		t.Errorf("got %q, want %q", name, "tree-1")
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

func TestEngineerNames_Size(t *testing.T) {
	if len(EngineerNames) != 20 {
		t.Errorf("EngineerNames has %d names, want 20", len(EngineerNames))
	}
}

func TestResearcherNames_Size(t *testing.T) {
	if len(ResearcherNames) != 15 {
		t.Errorf("ResearcherNames has %d names, want 15", len(ResearcherNames))
	}
}

func TestManagerNames_Size(t *testing.T) {
	if len(ManagerNames) != 15 {
		t.Errorf("ManagerNames has %d names, want 15", len(ManagerNames))
	}
}

func TestPartitionedPools_NoDuplicatesWithinPool(t *testing.T) {
	pools := map[string][]string{
		"EngineerNames":   EngineerNames,
		"ResearcherNames": ResearcherNames,
		"ManagerNames":    ManagerNames,
	}
	for poolName, pool := range pools {
		seen := make(map[string]bool)
		for _, name := range pool {
			if seen[name] {
				t.Errorf("duplicate name %q in %s", name, poolName)
			}
			seen[name] = true
		}
	}
}

func TestPartitionedPools_NoDuplicatesAcrossPools(t *testing.T) {
	seen := make(map[string]string)
	allPools := map[string][]string{
		"EngineerNames":   EngineerNames,
		"ResearcherNames": ResearcherNames,
		"ManagerNames":    ManagerNames,
	}
	for poolName, pool := range allPools {
		for _, name := range pool {
			if otherPool, exists := seen[name]; exists {
				t.Errorf("name %q appears in both %s and %s", name, otherPool, poolName)
			}
			seen[name] = poolName
		}
	}
}

func TestPartitionedPools_TotalCapacity(t *testing.T) {
	total := len(EngineerNames) + len(ResearcherNames) + len(ManagerNames)
	if total != 50 {
		t.Errorf("total pool capacity is %d, want 50", total)
	}
}

func TestNamePools_MapsAllTypes(t *testing.T) {
	expectedTypes := []string{"engineer", "researcher", "manager", "tester", "code-merger"}
	for _, typ := range expectedTypes {
		pool, ok := NamePools[typ]
		if !ok {
			t.Errorf("NamePools missing entry for type %q", typ)
			continue
		}
		if len(pool) == 0 {
			t.Errorf("NamePools[%q] is empty", typ)
		}
	}
}

func TestNamePools_SharedPools(t *testing.T) {
	// tester and code-merger should share engineer pool
	if &NamePools["tester"][0] != &EngineerNames[0] {
		t.Error("tester pool should be the same slice as EngineerNames")
	}
	if &NamePools["code-merger"][0] != &EngineerNames[0] {
		t.Error("code-merger pool should be the same slice as EngineerNames")
	}
}

func TestFallbackPrefix_MapsAllTypes(t *testing.T) {
	expected := map[string]string{
		"engineer":    "tree",
		"researcher":  "river",
		"manager":     "peak",
		"tester":      "tree",
		"code-merger": "tree",
	}
	for typ, wantPrefix := range expected {
		got, ok := FallbackPrefix[typ]
		if !ok {
			t.Errorf("FallbackPrefix missing entry for type %q", typ)
			continue
		}
		if got != wantPrefix {
			t.Errorf("FallbackPrefix[%q] = %q, want %q", typ, got, wantPrefix)
		}
	}
}

func TestNamePool_IsUnionOfAllPools(t *testing.T) {
	// NamePool should contain all names from all three pools
	allNames := make(map[string]bool)
	for _, name := range EngineerNames {
		allNames[name] = true
	}
	for _, name := range ResearcherNames {
		allNames[name] = true
	}
	for _, name := range ManagerNames {
		allNames[name] = true
	}

	if len(NamePool) != len(allNames) {
		t.Errorf("NamePool has %d names, but union of pools has %d", len(NamePool), len(allNames))
	}

	poolSet := make(map[string]bool)
	for _, name := range NamePool {
		poolSet[name] = true
	}
	for name := range allNames {
		if !poolSet[name] {
			t.Errorf("name %q is in a typed pool but not in NamePool", name)
		}
	}
}
