package runtime

// QUM-724 — fault-banner next-action hint must steer operators to `wake`,
// not `recover`. This is a NEW assertion that lives alongside the existing
// TestClassifyBackendFault_MapsKnownSentinels (which still tests "recover"
// substring until the implementer flips the strings in unified.go).
//
// This test will fail until ClassifyBackendFault returns hints mentioning
// "wake" for the known sentinels and the unknown default.

import (
	"errors"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/backend"
)

func TestClassifyBackendFault_HintsWake(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "HangTimeout sentinel", err: backend.ErrHangTimeout},
		{name: "SubscriberWedged sentinel", err: backend.ErrSubscriberWedged},
		{name: "Unknown error", err: errors.New("some other backend fault")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, hint := ClassifyBackendFault(tc.err)
			if hint == "" {
				t.Fatal("hint is empty; expected operator-facing next-action string")
			}
			if !strings.Contains(strings.ToLower(hint), "wake") {
				t.Errorf("hint = %q, want it to mention `wake` (QUM-724 rename)", hint)
			}
			if strings.Contains(strings.ToLower(hint), "recover") {
				t.Errorf("hint = %q, must NOT mention `recover` after the QUM-724 rename", hint)
			}
		})
	}
}
