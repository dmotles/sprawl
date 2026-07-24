package hube2e

import (
	"fmt"
	"testing"
)

// checkContiguous is the invariant that underpins every zero-gap / zero-dupe
// claim in the capstone: the DATA-frame seqs a subscriber received on one
// connection must be strictly ascending by exactly one, with the first equal to
// resumeSeq+1 (resumeSeq is the from_seq the subscriber resumed at; 0 for a
// fresh subscribe). A nil error means contiguous with no gaps and no dupes. An
// empty slice is vacuously contiguous — callers assert non-emptiness separately
// when they expect frames.
func checkContiguous(seqs []int64, resumeSeq int64) error {
	if len(seqs) == 0 {
		return nil
	}
	if seqs[0] != resumeSeq+1 {
		return fmt.Errorf("first seq = %d, want %d (resume floor %d + 1)", seqs[0], resumeSeq+1, resumeSeq)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			return fmt.Errorf("non-contiguous at index %d: seq %d follows %d (want %d)",
				i, seqs[i], seqs[i-1], seqs[i-1]+1)
		}
	}
	return nil
}

// TestCheckContiguous_DetectsGap is the permanent negative control that keeps
// the contiguity assertion non-vacuous. It is intentionally untagged so it runs
// on every `make validate`, independent of the hub_e2e build tag.
func TestCheckContiguous_DetectsGap(t *testing.T) {
	cases := []struct {
		name      string
		seqs      []int64
		resumeSeq int64
		wantErr   bool
	}{
		{"clean run from zero", []int64{1, 2, 3}, 0, false},
		{"clean delta after resume", []int64{6, 7, 8}, 5, false},
		{"empty is vacuously ok", nil, 0, false},
		{"forward gap", []int64{1, 2, 4}, 0, true},
		{"duplicate seq", []int64{1, 2, 2, 3}, 0, true},
		{"non-monotonic", []int64{1, 3, 2}, 0, true},
		{"wrong resume floor", []int64{5, 6, 7}, 0, true},
		{"resume off by one", []int64{7, 8}, 5, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkContiguous(tc.seqs, tc.resumeSeq)
			if tc.wantErr && err == nil {
				t.Fatalf("checkContiguous(%v, %d) = nil, want an error", tc.seqs, tc.resumeSeq)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("checkContiguous(%v, %d) = %v, want nil", tc.seqs, tc.resumeSeq, err)
			}
		})
	}
}
