package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Execute() is the sink for EVERY error any cobra command returns, which makes
// it the single widest DSN carrier in the tree (QUM-1280): `store migrate`'s raw
// pgx failure, `store dispatch`'s DegradedError wrap, and every other RunE
// error all land on one Fprintln.
//
// Redaction happens at the PRINT, never at the return: sanitising the value
// where it is produced replaces the error with a string and breaks errors.Is for
// in-process callers — TestStoreDispatch_PropagatesAnOpenFailure depends on the
// identity surviving.
//
// The assertions are ABSENCE OF THE SECRET, not presence of a marker, paired
// with a survival assertion so that printing nothing cannot satisfy them.
func TestWriteExecError_RedactsTheDSN(t *testing.T) {
	var buf bytes.Buffer
	writeExecError(&buf, leakyPGXError())
	got := buf.String()
	if !strings.Contains(got, "invalid port") {
		t.Fatalf("the diagnosis did not survive, so the absence checks below would pass vacuously; got:\n%s", got)
	}
	for _, secret := range []string{probeDSNPassword, probeDSNHost, probeDSNURL} {
		if strings.Contains(got, secret) {
			t.Errorf("the Execute sink leaked %q; got:\n%s", secret, got)
		}
	}
}

// Redaction must not mangle the multi-line `next:` hints every command in this
// repo relies on — QUM-1086's config error is a 24-line key reference. An error
// message made useless is one somebody deletes the redaction to fix.
func TestWriteExecError_PreservesMultiLineHints(t *testing.T) {
	err := errors.New("the event log is unreachable, so dispatch cannot start: dial refused\nnext: run `sprawl store doctor` to diagnose the connection")
	var buf bytes.Buffer
	writeExecError(&buf, err)
	if got := buf.String(); got != err.Error()+"\n" {
		t.Errorf("a DSN-free multi-line error was altered:\nwant: %q\ngot:  %q", err.Error()+"\n", got)
	}
}

// RedactError(nil) is "", so an unguarded Fprintln would emit a blank line.
func TestWriteExecError_NilPrintsNothing(t *testing.T) {
	var buf bytes.Buffer
	writeExecError(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("writeExecError(nil) wrote %q", buf.String())
	}
}

func TestExecuteTo_ReportsFailureAndRedacts(t *testing.T) {
	failing := &cobra.Command{
		Use: "probe", SilenceErrors: true, SilenceUsage: true,
		RunE: func(*cobra.Command, []string) error { return fmt.Errorf("migrate failed: %w", leakyPGXError()) },
	}
	var buf bytes.Buffer
	failing.SetOut(&buf)
	failing.SetErr(&buf)
	if code := executeTo(&buf, failing); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	got := buf.String()
	if !strings.Contains(got, "migrate failed") {
		t.Fatalf("the diagnosis did not survive; got:\n%s", got)
	}
	for _, secret := range []string{probeDSNPassword, probeDSNHost, probeDSNURL} {
		if strings.Contains(got, secret) {
			t.Errorf("executeTo leaked %q; got:\n%s", secret, got)
		}
	}

	// Control, in the other direction: a command that succeeds must report 0
	// and print nothing, or the row above would be satisfied by a sink that
	// always prints something.
	ok := &cobra.Command{Use: "probe", SilenceErrors: true, RunE: func(*cobra.Command, []string) error { return nil }}
	var okBuf bytes.Buffer
	ok.SetOut(&okBuf)
	ok.SetErr(&okBuf)
	if code := executeTo(&okBuf, ok); code != 0 || okBuf.Len() != 0 {
		t.Errorf("a successful command returned code %d and wrote %q", code, okBuf.String())
	}
}

// SilenceErrors is load-bearing for the redaction, not just for output tidiness:
// cobra's own error print goes straight to OutOrStderr and bypasses
// writeExecError entirely, so removing the flag reopens the leak while every
// test above stays green.
func TestRootCmd_SilencesCobrasOwnErrorPrint(t *testing.T) {
	if !rootCmd.SilenceErrors {
		t.Error("rootCmd.SilenceErrors is false, so cobra prints the raw, UNREDACTED error itself before Execute's sink ever sees it")
	}
}
