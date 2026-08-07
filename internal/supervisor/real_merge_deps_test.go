package supervisor

import (
	"testing"

	"github.com/dmotles/sprawl/internal/merge"
)

// TestNewReal_MergeDepsBindEveryGitSeam — QUM-1090 wiring gate. The MCP
// `merge` and `retire --merge` verbs reach the merge engine through the
// NewMergeDeps closure built in NewReal. That closure is separate from the
// CLI's (cmd/merge.go), so binding a new seam in one and not the other
// leaves every MCP-initiated merge silently without it while
// internal/merge's own tests stay green — they inject their own fakes and
// cannot see this site at all.
//
// Reflecting over the struct rather than naming fields means a seam added
// later is covered without anyone remembering to update this test.
// Negative control: drop any binding from merge.RealDeps and this names it.
func TestNewReal_MergeDepsBindEveryGitSeam(t *testing.T) {
	sup, err := NewReal(Config{SprawlRoot: t.TempDir(), CallerName: "weave"})
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}

	for _, tc := range []struct {
		name string
		make func() *merge.Deps
	}{
		// retireDeps no longer constructs merge.Deps: QUM-1087/QUM-1088 removed
		// the retire path's own merge, so there is exactly ONE construction
		// site left. That is the point of the change, not a gap in this test —
		// the drift this guards against (a seam bound in one site and forgotten
		// in the other) cannot happen with one site.
		{"mergeDeps.NewMergeDeps", sup.mergeDeps.NewMergeDeps},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.make()
			missing, checked := merge.NilSeams(d)
			if len(missing) != 0 {
				t.Errorf("%s left merge.Deps seams nil: %v", tc.name, missing)
			}
			if checked < merge.MinDepsSeams {
				t.Errorf("NilSeams examined %d seams, want >= %d; the walk looks broken", checked, merge.MinDepsSeams)
			}
			// Stderr is not a func, so the walk above skips it — and it is
			// the ONE value that can still differ between the two sites
			// after the RealDeps extraction. Merge writes to it
			// unconditionally, so nil is a panic.
			if d.Stderr == nil {
				t.Errorf("%s left merge.Deps.Stderr nil", tc.name)
			}
		})
	}
}
