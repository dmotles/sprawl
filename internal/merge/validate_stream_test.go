package merge

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fixedNow returns a now-func that always returns the same UTC instant.
func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// realNow is a stable now-func for tests that don't care about the exact
// timestamp but want a valid one.
func realNow() func() time.Time {
	t := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// readFile returns the contents of path as a string, failing the test on error.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(b)
}

func TestOpenValidateLog_CreatesFileUnderLogsDir(t *testing.T) {
	root := t.TempDir()
	// Pre-create .sprawl/logs to make sure Open is happy when it exists.
	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}

	v, err := OpenValidateLog(root, nil, realNow())
	if err != nil {
		t.Fatalf("OpenValidateLog: %v", err)
	}
	defer v.Finish(nil)

	got := v.Path()
	wantPrefix := filepath.Join(root, ".sprawl", "logs") + string(os.PathSeparator)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("Path()=%q, want prefix %q", got, wantPrefix)
	}
	base := filepath.Base(got)
	re := regexp.MustCompile(`^validate-\d{8}-\d{6}\.log$`)
	if !re.MatchString(base) {
		t.Errorf("filename %q does not match %s", base, re)
	}

	info, err := os.Stat(filepath.Dir(got))
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if info.Mode().Perm()&0o700 != 0o700 {
		t.Errorf("parent perm %o lacks 0700 bits", info.Mode().Perm())
	}
}

func TestOpenValidateLog_AutoCreatesLogsDir(t *testing.T) {
	root := t.TempDir()
	logsDir := filepath.Join(root, ".sprawl", "logs")
	if _, err := os.Stat(logsDir); !os.IsNotExist(err) {
		t.Fatalf("precondition: logs dir should not exist yet, stat err=%v", err)
	}

	v, err := OpenValidateLog(root, nil, realNow())
	if err != nil {
		t.Fatalf("OpenValidateLog: %v", err)
	}
	defer v.Finish(nil)

	if _, err := os.Stat(logsDir); err != nil {
		t.Errorf("logs dir not auto-created: %v", err)
	}
}

func TestValidateLog_WriteAppendsLineAndForwardsToSink(t *testing.T) {
	root := t.TempDir()
	var got []string
	sink := func(s string) { got = append(got, s) }

	v, err := OpenValidateLog(root, sink, realNow())
	if err != nil {
		t.Fatalf("OpenValidateLog: %v", err)
	}

	v.Write("a")
	v.Write("b")
	v.Write("c")
	v.Finish(nil)

	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("sink received %d lines, want %d (%v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("sink[%d]=%q, want %q", i, got[i], w)
		}
	}

	contents := readFile(t, v.Path())
	if !strings.HasPrefix(contents, "a\nb\nc\n") {
		t.Errorf("file contents=%q, want prefix \"a\\nb\\nc\\n\"", contents)
	}
}

func TestValidateLog_SinkReturnsSameSemantics(t *testing.T) {
	root := t.TempDir()
	var got []string
	wrap := func(s string) { got = append(got, s) }

	v, err := OpenValidateLog(root, wrap, realNow())
	if err != nil {
		t.Fatalf("OpenValidateLog: %v", err)
	}

	s := v.Sink()
	s("a")
	s("b")
	s("c")
	v.Finish(nil)

	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("sink received %d lines, want %d (%v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("sink[%d]=%q, want %q", i, got[i], w)
		}
	}

	contents := readFile(t, v.Path())
	if !strings.HasPrefix(contents, "a\nb\nc\n") {
		t.Errorf("file contents=%q, want prefix \"a\\nb\\nc\\n\"", contents)
	}
}

func TestValidateLog_FinishWritesExitTrailerOnSuccess(t *testing.T) {
	root := t.TempDir()
	v, err := OpenValidateLog(root, nil, realNow())
	if err != nil {
		t.Fatalf("OpenValidateLog: %v", err)
	}
	v.Finish(nil)

	contents := readFile(t, v.Path())
	if !strings.HasSuffix(contents, "[exit=0]\n") {
		t.Errorf("file contents=%q, want suffix \"[exit=0]\\n\"", contents)
	}
}

func TestValidateLog_FinishWritesErrorTrailerOnFailure(t *testing.T) {
	root := t.TempDir()
	v, err := OpenValidateLog(root, nil, realNow())
	if err != nil {
		t.Fatalf("OpenValidateLog: %v", err)
	}
	v.Write("line1")
	v.Finish(errors.New("validation failed: tests fail"))

	contents := readFile(t, v.Path())
	want := "line1\n[error=validation failed: tests fail]\n"
	if !strings.HasSuffix(contents, want) {
		t.Errorf("file contents=%q, want suffix %q", contents, want)
	}
}

func TestValidateLog_FinishIsIdempotent(t *testing.T) {
	root := t.TempDir()
	v, err := OpenValidateLog(root, nil, realNow())
	if err != nil {
		t.Fatalf("OpenValidateLog: %v", err)
	}

	v.Finish(nil)
	afterFirst := readFile(t, v.Path())

	// Second call must not panic and must not append another trailer.
	v.Finish(nil)
	afterSecond := readFile(t, v.Path())

	if afterFirst != afterSecond {
		t.Errorf("second Finish mutated file:\nfirst=%q\nsecond=%q", afterFirst, afterSecond)
	}
	if strings.Count(afterSecond, "[exit=0]") != 1 {
		t.Errorf("trailer appeared %d times, want 1; contents=%q",
			strings.Count(afterSecond, "[exit=0]"), afterSecond)
	}
}

func TestValidateLog_NilSinkSafe(t *testing.T) {
	root := t.TempDir()
	v, err := OpenValidateLog(root, nil, realNow())
	if err != nil {
		t.Fatalf("OpenValidateLog: %v", err)
	}

	// Must not panic with nil sink.
	v.Write("x")
	v.Finish(nil)

	contents := readFile(t, v.Path())
	if !strings.HasPrefix(contents, "x\n") {
		t.Errorf("file contents=%q, want prefix \"x\\n\"", contents)
	}
}

func TestValidateLog_FilenameDeterministicFromNow(t *testing.T) {
	root := t.TempDir()
	now := fixedNow(time.Date(2026, 5, 18, 14, 7, 33, 0, time.UTC))

	v, err := OpenValidateLog(root, nil, now)
	if err != nil {
		t.Fatalf("OpenValidateLog: %v", err)
	}
	defer v.Finish(nil)

	want := "validate-20260518-140733.log"
	if got := filepath.Base(v.Path()); got != want {
		t.Errorf("filename=%q, want %q", got, want)
	}
}
