package runtime

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestRuntimeDoesNotImportTUI asserts that no .go file in this package
// imports internal/tui. The runtime package must not depend on the TUI;
// the TUI adapter (which bridges runtime <-> tui) lives in a separate
// package (internal/tuiruntime). See QUM-431.
func TestRuntimeDoesNotImportTUI(t *testing.T) {
	t.Helper()

	const forbidden = "github.com/dmotles/sprawl/internal/tui"

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	var offenders []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
				offenders = append(offenders, e.Name()+" imports "+path)
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("internal/runtime must not import internal/tui:\n  %s", strings.Join(offenders, "\n  "))
	}
}
