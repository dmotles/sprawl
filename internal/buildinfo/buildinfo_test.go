package buildinfo

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// statRecorder is a path-aware stand-in for os.Stat. It is NOT a mock of
// /proc/self/exe: classifyImage is a pure classifier over an already-read link
// string, and the real read lives in Image(), exercised against a real deleted
// inode by TestImage_DeletedRealBinaryIsDetected below.
type statRecorder struct {
	exists map[string]bool
	seen   []string
}

func (s *statRecorder) stat(path string) (os.FileInfo, error) {
	s.seen = append(s.seen, path)
	if s.exists[path] {
		return nil, nil
	}
	return nil, os.ErrNotExist
}

func statOf(paths ...string) *statRecorder {
	m := make(map[string]bool, len(paths))
	for _, p := range paths {
		m[p] = true
	}
	return &statRecorder{exists: m}
}

// commitRecorder records which path the on-disk commit reader was handed. It
// must be the STRIPPED path: handing it the literal "(deleted)" link text
// makes the read fail and guts AC2 in exactly the make-install case.
type commitRecorder struct {
	commit string
	err    error
	seen   []string
}

func (c *commitRecorder) read(path string) (string, error) {
	c.seen = append(c.seen, path)
	return c.commit, c.err
}

func commitOf(c string) *commitRecorder { return &commitRecorder{commit: c} }

func commitFn(c string) func(string) (string, error) { return commitOf(c).read }

func errCommitFn(string) (string, error) { return "", os.ErrNotExist }

func TestClassifyImage_Clean(t *testing.T) {
	got := classifyImage("/usr/local/bin/sprawl", nil, "abc123",
		statOf("/usr/local/bin/sprawl").stat, commitFn("abc123"))
	if got.ExePath != "/usr/local/bin/sprawl" {
		t.Errorf("ExePath = %q, want /usr/local/bin/sprawl", got.ExePath)
	}
	if got.ExeCheck != "ok" {
		t.Errorf("ExeCheck = %q, want ok", got.ExeCheck)
	}
	if got.CommitCheck != "match" {
		t.Errorf("CommitCheck = %q, want match", got.CommitCheck)
	}
	if got.Stale {
		t.Errorf("Stale = true, want false for a clean image")
	}
	if got.Detail != "" {
		t.Errorf("Detail = %q, want empty for a clean image (stay quiet)", got.Detail)
	}
}

// The `make install` case: the running image is marked (deleted) AND a fresh
// binary now occupies the stripped path. An implementation that stats the
// stripped path instead of the literal link text sees "it exists" and reports
// clean — in precisely the scenario this issue was filed about.
func TestClassifyImage_DeletedWhileStrippedPathExists(t *testing.T) {
	st := statOf("/usr/local/bin/sprawl") // replacement present; link text absent
	cr := commitOf("def456")
	got := classifyImage("/usr/local/bin/sprawl (deleted)", nil, "abc123", st.stat, cr.read)
	if got.ExeCheck != "deleted" {
		t.Errorf("ExeCheck = %q, want deleted", got.ExeCheck)
	}
	if !got.Stale {
		t.Errorf("Stale = false, want true (a replacement at the stripped path is the defect, not the all-clear)")
	}
	if got.ExePath != "/usr/local/bin/sprawl" {
		t.Errorf("ExePath = %q, want the suffix stripped", got.ExePath)
	}
	if got.OnDiskCommit != "def456" {
		t.Errorf("OnDiskCommit = %q, want def456", got.OnDiskCommit)
	}
	if got.CommitCheck != "differ" {
		t.Errorf("CommitCheck = %q, want differ", got.CommitCheck)
	}
	if !strings.Contains(got.Detail, "(deleted)") {
		t.Errorf("Detail = %q, want it to quote the raw link text", got.Detail)
	}
	if !strings.Contains(got.Detail, "abc123") || !strings.Contains(got.Detail, "def456") {
		t.Errorf("Detail = %q, want both commits named", got.Detail)
	}
	// The disambiguating stat must interrogate the LITERAL link text. Stating
	// only the stripped path cannot tell "deleted" from "a file really named
	// that", and gets the case above backwards.
	var sawLiteral bool
	for _, p := range st.seen {
		if p == "/usr/local/bin/sprawl (deleted)" {
			sawLiteral = true
		}
	}
	if !sawLiteral {
		t.Errorf("stat calls = %v, want the literal link text stat'ed", st.seen)
	}
	// The commit reader, conversely, must be handed the STRIPPED path — the
	// replacement binary. Handing it the "(deleted)" text makes the read fail
	// and AC2 silently reports on_disk_commit as unknown.
	if len(cr.seen) != 1 || cr.seen[0] != "/usr/local/bin/sprawl" {
		t.Errorf("on-disk commit read from %v, want [/usr/local/bin/sprawl]", cr.seen)
	}
}

// A same-commit reinstall still replaces the running image. Staleness must not
// collapse into a commit comparison.
func TestClassifyImage_DeletedSameCommitStillStale(t *testing.T) {
	got := classifyImage("/usr/local/bin/sprawl (deleted)", nil, "abc123",
		statOf().stat, commitFn("abc123"))
	if got.CommitCheck != "match" {
		t.Errorf("CommitCheck = %q, want match", got.CommitCheck)
	}
	if !got.Stale {
		t.Errorf("Stale = false, want true (the deleted marker is authoritative on its own)")
	}
}

// A path that genuinely ends in " (deleted)" and still exists is not stale.
// This is what makes the probe measure the process rather than a string.
func TestClassifyImage_LiterallyNamedDeletedIsNotStale(t *testing.T) {
	got := classifyImage("/opt/sprawl (deleted)", nil, "abc123",
		statOf("/opt/sprawl (deleted)").stat, commitFn("abc123"))
	if got.ExeCheck != "ok" {
		t.Errorf("ExeCheck = %q, want ok (the file exists under that literal name)", got.ExeCheck)
	}
	if got.Stale {
		t.Errorf("Stale = true, want false")
	}
	if got.ExePath != "/opt/sprawl (deleted)" {
		t.Errorf("ExePath = %q, want the literal path preserved", got.ExePath)
	}
}

// " (deleted)" mid-path is a directory name, not the kernel's marker.
func TestClassifyImage_DeletedSubstringMidPathIsNotAMarker(t *testing.T) {
	got := classifyImage("/opt/old (deleted)/sprawl", nil, "abc123",
		statOf("/opt/old (deleted)/sprawl").stat, commitFn("abc123"))
	if got.ExeCheck != "ok" {
		t.Errorf("ExeCheck = %q, want ok (marker is a suffix, not a substring)", got.ExeCheck)
	}
	if got.Stale {
		t.Errorf("Stale = true, want false")
	}
}

// The rename(2)-swap variant: `install` renames a new binary onto the same
// path, so the inode differs but nothing is ever marked (deleted). The commit
// comparison is the only defence against this one.
func TestClassifyImage_RenameSwapCaughtByCommitOnly(t *testing.T) {
	got := classifyImage("/usr/local/bin/sprawl", nil, "abc123",
		statOf("/usr/local/bin/sprawl").stat, commitFn("def456"))
	if got.ExeCheck != "ok" {
		t.Errorf("ExeCheck = %q, want ok (no deleted marker in a rename swap)", got.ExeCheck)
	}
	if got.CommitCheck != "differ" {
		t.Errorf("CommitCheck = %q, want differ", got.CommitCheck)
	}
	if !got.Stale {
		t.Errorf("Stale = false, want true")
	}
}

func TestClassifyImage_ReadlinkErrorIsLoud(t *testing.T) {
	got := classifyImage("", errors.New("permission denied"), "abc123",
		statOf().stat, commitFn("abc123"))
	if got.ExeCheck != "unavailable" {
		t.Errorf("ExeCheck = %q, want unavailable", got.ExeCheck)
	}
	if got.Stale {
		t.Errorf("Stale = true, want false (an unrunnable check is not evidence of staleness)")
	}
	if !strings.Contains(got.Detail, "permission denied") {
		t.Errorf("Detail = %q, want the underlying error named", got.Detail)
	}
	if !strings.Contains(got.Detail, notVerified) {
		t.Errorf("Detail = %q, want it to contain %q", got.Detail, notVerified)
	}
}

// An empty link with no error is not a clean image, it is a broken read.
func TestClassifyImage_EmptyLinkWithoutErrorIsUnavailable(t *testing.T) {
	got := classifyImage("", nil, "abc123", statOf().stat, commitFn("abc123"))
	if got.ExeCheck != "unavailable" {
		t.Errorf("ExeCheck = %q, want unavailable for an empty link", got.ExeCheck)
	}
	if got.Stale {
		t.Errorf("Stale = true, want false")
	}
	if !strings.Contains(got.Detail, notVerified) {
		t.Errorf("Detail = %q, want it to contain %q", got.Detail, notVerified)
	}
}

func TestClassifyImage_OnDiskGone(t *testing.T) {
	got := classifyImage("/usr/local/bin/sprawl (deleted)", nil, "abc123",
		statOf().stat, errCommitFn)
	if got.OnDiskCommit != "" {
		t.Errorf("OnDiskCommit = %q, want empty", got.OnDiskCommit)
	}
	if got.CommitCheck != "unknown" {
		t.Errorf("CommitCheck = %q, want unknown", got.CommitCheck)
	}
	if !got.Stale {
		t.Errorf("Stale = false, want true")
	}
}

// An unstamped dev build must not manufacture a permanent false divergence.
func TestClassifyImage_UnknownRunningCommitNeverDiffers(t *testing.T) {
	for _, running := range []string{"", "none", "dev"} {
		got := classifyImage("/usr/local/bin/sprawl", nil, running,
			statOf("/usr/local/bin/sprawl").stat, commitFn("abc123"))
		if got.CommitCheck != "unknown" {
			t.Errorf("running=%q: CommitCheck = %q, want unknown", running, got.CommitCheck)
		}
		if got.Stale {
			t.Errorf("running=%q: Stale = true, want false", running)
		}
		if !strings.Contains(got.Detail, notVerified) {
			t.Errorf("running=%q: Detail = %q, want it to contain %q", running, got.Detail, notVerified)
		}
	}
}

// An absent field reads as "fine". The verdict fields must always be emitted —
// including on_disk_commit, which is the field AC2 is about.
func TestImageStatus_JSONNeverOmitsVerdictFields(t *testing.T) {
	raw, err := json.Marshal(ImageStatus{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"exe_path", "exe_check", "running_commit", "on_disk_commit", "commit_check", "stale"} {
		if _, ok := m[k]; !ok {
			t.Errorf("key %q missing from %s — an absent verdict field reads as fine", k, raw)
		}
	}
	// detail must stay omitempty: its absence is how a clean image stays quiet.
	if _, ok := m["detail"]; ok {
		t.Errorf("detail emitted for a zero ImageStatus; it must be omitempty\n%s", raw)
	}
}

func TestClassifyImage_DetailRequiredWhenNotClean(t *testing.T) {
	cases := map[string]ImageStatus{
		"deleted":     classifyImage("/usr/local/bin/sprawl (deleted)", nil, "abc123", statOf("/usr/local/bin/sprawl").stat, commitFn("def456")),
		"renameswap":  classifyImage("/usr/local/bin/sprawl", nil, "abc123", statOf("/usr/local/bin/sprawl").stat, commitFn("def456")),
		"unavailable": classifyImage("", errors.New("boom"), "abc123", statOf().stat, commitFn("abc123")),
		"ondiskgone":  classifyImage("/usr/local/bin/sprawl (deleted)", nil, "abc123", statOf().stat, errCommitFn),
		"unstamped":   classifyImage("/usr/local/bin/sprawl", nil, "none", statOf("/usr/local/bin/sprawl").stat, commitFn("abc123")),
	}
	for name, got := range cases {
		if got.Detail == "" {
			t.Errorf("%s: Detail empty, want a human-readable explanation", name)
		}
	}
}

// Negative-control direction (subject known clean, the probe must stay quiet):
// Image() reads the real /proc/self/exe of this very test process — no mock, no
// override. The test binary exists on disk, so the verdict must be "ok".
func TestImage_RealProcessIsNotStale(t *testing.T) {
	got := Image()
	if got.ExePath == "" {
		t.Errorf("ExePath empty, want this test binary's path")
	}
	if got.ExeCheck != "ok" {
		t.Errorf("ExeCheck = %q, want ok (this test binary has not been deleted): %+v", got.ExeCheck, got)
	}
	if got.Stale {
		t.Errorf("Stale = true, want false: %+v", got)
	}
	if got.RunningCommit != Commit() {
		t.Errorf("RunningCommit = %q, want the linker stamp %q", got.RunningCommit, Commit())
	}
	if got.CommitCheck != "match" && got.Detail == "" {
		t.Errorf("CommitCheck = %q with an empty Detail — a non-match must be explained: %+v", got.CommitCheck, got)
	}
}

const childEnv = "SPRAWL_BUILDINFO_CHILD_PROBE"

// TestChildImageProbe is not a test. It is the child half of
// TestImage_DeletedRealBinaryIsDetected: when re-executed with the guard env
// set, it waits for its own binary to be removed and prints the real Image()
// verdict. Inert under a normal `go test` run.
func TestChildImageProbe(t *testing.T) {
	signal := os.Getenv(childEnv)
	if signal == "" {
		t.Skip("child probe: not invoked as a child")
	}
	deadline := time.Now().Add(20 * time.Second)
	var released bool
	for time.Now().Before(deadline) {
		if _, err := os.Stat(signal); err == nil {
			released = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !released {
		t.Fatalf("child probe: parent never signalled within 20s (%s absent)", signal)
	}
	raw, err := json.Marshal(Image())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Written to stderr so the parent can find it regardless of test framing.
	if _, err := io.WriteString(os.Stderr, "\nIMAGESTATUS:"+string(raw)+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// Positive-control direction (subject where the defect IS present, the probe
// MUST fire): a real copy of this test binary is executed, then REPLACED on
// disk out from under the running process — unlink followed by a new file at
// the same path, which is what `make install` does. Real inode, real deletion,
// real /proc/self/exe. Nothing is mocked.
//
// The replacement is what makes this a control rather than decoration. Go's
// os.Executable() strips the " (deleted)" marker (os/executable_procfs.go), so
// an implementation built on it stats the freshly-installed replacement, finds
// it present, and reports a confident false clean — passing every assertion
// here if the path were left merely absent.
func TestImage_DeletedRealBinaryIsDetected(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "probe")
	src, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	if err := os.WriteFile(copyPath, src, 0o755); err != nil {
		t.Fatalf("write binary copy: %v", err)
	}
	signal := filepath.Join(dir, "go-ahead")

	cmd := exec.Command(copyPath, "-test.run=^TestChildImageProbe$", "-test.v")
	cmd.Env = append(os.Environ(), childEnv+"="+signal)
	// Both streams: the child reports via stderr, but a child t.Fatalf lands
	// on stdout and is the diagnostic we would need most.
	var out strings.Builder
	cmd.Stderr = &out
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	reaped := false
	defer func() {
		if !reaped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	// Replace the running image: unlink, then put a different file back at the
	// same path. This is the `make install` shape.
	if err := os.Remove(copyPath); err != nil {
		t.Fatalf("remove running child binary: %v", err)
	}
	if err := os.WriteFile(copyPath, []byte("replacement binary"), 0o755); err != nil {
		t.Fatalf("write replacement binary: %v", err)
	}
	if err := os.WriteFile(signal, []byte("go"), 0o644); err != nil {
		t.Fatalf("write signal: %v", err)
	}
	err = cmd.Wait()
	reaped = true
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, out.String())
	}

	_, tail, ok := strings.Cut(out.String(), "IMAGESTATUS:")
	if !ok {
		t.Fatalf("child emitted no verdict:\n%s", out.String())
	}
	line, _, _ := strings.Cut(tail, "\n")
	var got ImageStatus
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("decode child verdict %q: %v", line, err)
	}
	if got.ExeCheck != "deleted" {
		t.Errorf("ExeCheck = %q, want deleted for a genuinely deleted running image: %+v", got.ExeCheck, got)
	}
	if !got.Stale {
		t.Errorf("Stale = false, want true: %+v", got)
	}
	if got.ExePath != copyPath {
		t.Errorf("ExePath = %q, want %q with the marker stripped", got.ExePath, copyPath)
	}
	if got.Detail == "" {
		t.Errorf("Detail empty on a stale image: %+v", got)
	}
}

// buildFixtureBinary compiles a throwaway main package, mirroring the repo's
// own stamping shape (Makefile: -X main.commit=...). It never runs the result.
func buildFixtureBinary(t *testing.T, ldflags string, gitInit bool) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain unavailable: %v", err) // unmet precondition, not a pass
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module probe\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	// `commit` is declared and referenced so -X actually stamps a symbol, as
	// it does in main.go. Note the sanctioned read is still the recorded
	// -ldflags build setting, which debug/buildinfo exposes without executing
	// anything; the data-section value is not reachable from outside.
	src := "package main\n\nimport \"fmt\"\n\nvar commit = \"none\"\n\nfunc main() { fmt.Println(commit) }\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if gitInit {
		// Make the fixture a real checkout so `go build` records vcs.revision.
		// Without this, "no vcs fallback" and "the fallback found nothing" are
		// indistinguishable.
		for _, args := range [][]string{
			{"init", "-q"},
			{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "fixture"},
		} {
			g := exec.Command("git", args...)
			g.Dir = dir
			if out, err := g.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
	}
	bin := filepath.Join(dir, "probe")
	args := []string{"build"}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	args = append(args, "-o", bin, ".")
	build := exec.Command("go", args...)
	build.Dir = dir
	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture binary: %v\n%s", err, out)
	}
	return bin
}

// onDiskCommit is the AC2 reader: it must recover a binary's commit WITHOUT
// executing it. Exercised against a real linker-stamped binary.
func TestOnDiskCommit_ReadsLdflagsStampWithoutExecuting(t *testing.T) {
	bin := buildFixtureBinary(t, "-X main.commit=cafebabe1234", false)
	got, err := onDiskCommit(bin)
	if err != nil {
		t.Fatalf("onDiskCommit: %v", err)
	}
	if got != "cafebabe1234" {
		t.Errorf("onDiskCommit = %q, want cafebabe1234", got)
	}
}

// An unstamped binary must report no commit rather than inventing one. The
// fixture is a real git checkout, so `go build` DOES record vcs.revision — the
// reader must still return "", because comparing a vcs.revision on disk to an
// -X stamp in the running process compares two different provenances and
// reports `differ` on identical source.
func TestOnDiskCommit_UnstampedBinaryHasNoCommit(t *testing.T) {
	bin := buildFixtureBinary(t, "", true)
	got, err := onDiskCommit(bin)
	if err != nil {
		t.Fatalf("onDiskCommit: %v", err)
	}
	if got != "" {
		t.Errorf("onDiskCommit = %q, want empty for an unstamped binary", got)
	}
}

func TestOnDiskCommit_MissingFileErrors(t *testing.T) {
	if _, err := onDiskCommit(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Errorf("onDiskCommit on a missing path returned nil error")
	}
}

func TestSetAndAccessors(t *testing.T) {
	t.Cleanup(func() { Set("dev", "none", "unknown") })
	// Establish the baseline explicitly rather than asserting virgin global
	// state — otherwise this goes spuriously red the moment a test that calls
	// Set is inserted above it.
	Set("dev", "none", "unknown")
	if Version() != "dev" || Commit() != "none" || Date() != "unknown" {
		t.Errorf("defaults = %q/%q/%q, want dev/none/unknown", Version(), Commit(), Date())
	}
	Set("v9.9.9", "deadbeef", "2026-01-01T00:00:00Z")
	if Version() != "v9.9.9" || Commit() != "deadbeef" || Date() != "2026-01-01T00:00:00Z" {
		t.Errorf("after Set = %q/%q/%q", Version(), Commit(), Date())
	}
}

// Set is written from main() and the accessors are read from MCP
// request-handler goroutines. This test carries no t.Errorf: it can only fail
// under -race (which `make validate` always uses), and the way to watch it
// fail is to drop the mutex from the accessors.
func TestAccessorsAreRaceSafe(t *testing.T) {
	t.Cleanup(func() { Set("dev", "none", "unknown") })
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 20 {
			_ = Commit()
			_ = Image()
		}
	}()
	for range 20 {
		Set("v1", "c1", "d1")
	}
	<-done
}
