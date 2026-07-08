package attach

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Minimal magic-byte headers recognized by http.DetectContentType.
var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	jpegBytes = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00")
	gifBytes  = []byte("GIF89a")
	webpBytes = []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
	txtBytes  = []byte("this is plain text, not an image at all\n")
)

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantPaths  []string
		wantPrompt string
	}{
		{"single path + quoted prompt", `/p/img.png "review this"`, []string{"/p/img.png"}, "review this"},
		{"multiple paths + quoted prompt", `a.png b.png "compare these"`, []string{"a.png", "b.png"}, "compare these"},
		{"path only, empty prompt", `/p/img.png`, []string{"/p/img.png"}, ""},
		{"prompt with inner spaces", `x.png "what is wrong here?"`, []string{"x.png"}, "what is wrong here?"},
		{"path with dot and tilde stays literal", `~/pics/a.b.png "hi"`, []string{"~/pics/a.b.png"}, "hi"},
		{"prompt only, no path", `"just a prompt"`, nil, "just a prompt"},
		{"tokens after closing quote are paths", `a.png "p" b.png`, []string{"a.png", "b.png"}, "p"},
		{"unterminated quote taken as prompt rest", `x.png "unclosed prompt`, []string{"x.png"}, "unclosed prompt"},
		{"empty quoted prompt", `x.png ""`, []string{"x.png"}, ""},
		{"empty", ``, nil, ""},
		{"whitespace only", `   `, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths, prompt := ParseArgs(tt.raw)
			if prompt != tt.wantPrompt {
				t.Errorf("prompt = %q, want %q", prompt, tt.wantPrompt)
			}
			if len(paths) != len(tt.wantPaths) {
				t.Fatalf("paths = %v, want %v", paths, tt.wantPaths)
			}
			for i := range paths {
				if paths[i] != tt.wantPaths[i] {
					t.Errorf("paths[%d] = %q, want %q", i, paths[i], tt.wantPaths[i])
				}
			}
		})
	}
}

func TestBuild_SniffMediaType(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		file      string
		data      []byte
		wantMedia string
	}{
		{"a.png", pngBytes, "image/png"},
		{"a.jpg", jpegBytes, "image/jpeg"},
		{"a.gif", gifBytes, "image/gif"},
		{"a.webp", webpBytes, "image/webp"},
	}
	for _, tt := range tests {
		t.Run(tt.wantMedia, func(t *testing.T) {
			p := writeFile(t, dir, tt.file, tt.data)
			blocks, chips, err := Build([]string{p}, "hi")
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			img := blocks[0]
			if img.Type != "image" || img.Source == nil {
				t.Fatalf("first block not an image: %+v", img)
			}
			if img.Source.MediaType != tt.wantMedia {
				t.Errorf("media_type = %q, want %q", img.Source.MediaType, tt.wantMedia)
			}
			if img.Source.Type != "base64" {
				t.Errorf("source type = %q, want base64", img.Source.Type)
			}
			// The base64 payload must round-trip to the exact file bytes (guards
			// against encoding the wrong file / truncated content).
			decoded, derr := base64.StdEncoding.DecodeString(img.Source.Data)
			if derr != nil {
				t.Fatalf("base64 decode: %v", derr)
			}
			if string(decoded) != string(tt.data) {
				t.Errorf("decoded data does not match original file bytes")
			}
			if chips[0].MediaType != tt.wantMedia {
				t.Errorf("chip media_type = %q, want %q", chips[0].MediaType, tt.wantMedia)
			}
		})
	}
}

// A JPEG whose filename claims .png must be sent as image/jpeg (content-sniff,
// not extension). This is the QUM-860 wrong-extension acceptance criterion.
func TestBuild_WrongExtensionUsesContentSniff(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "mislabeled.png", jpegBytes)
	blocks, chips, err := Build([]string{p}, "look")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := blocks[0].Source.MediaType; got != "image/jpeg" {
		t.Errorf("media_type = %q, want image/jpeg (sniffed, not from .png ext)", got)
	}
	if chips[0].MediaType != "image/jpeg" {
		t.Errorf("chip media_type = %q, want image/jpeg", chips[0].MediaType)
	}
}

func TestBuild_Rejects(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		_, _, err := Build([]string{filepath.Join(dir, "nope.png")}, "x")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
		if !strings.Contains(err.Error(), "cannot read") {
			t.Errorf("error = %q, want it to mention 'cannot read'", err.Error())
		}
	})

	t.Run("unreadable file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root; permission bits do not restrict reads")
		}
		p := writeFile(t, dir, "locked.png", pngBytes)
		if err := os.Chmod(p, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
		_, _, err := Build([]string{p}, "x")
		if err == nil {
			t.Fatal("expected error for unreadable file")
		}
	})

	t.Run("unsupported format", func(t *testing.T) {
		p := writeFile(t, dir, "notes.txt", txtBytes)
		_, _, err := Build([]string{p}, "x")
		if err == nil {
			t.Fatal("expected error for unsupported format")
		}
		if !strings.Contains(err.Error(), "unsupported format") {
			t.Errorf("error = %q, want it to mention 'unsupported format'", err.Error())
		}
	})

	t.Run("oversized file", func(t *testing.T) {
		p := writeFile(t, dir, "big.png", pngBytes)
		// Grow to just over the 10 MB cap using a sparse truncate (cheap).
		if err := os.Truncate(p, MaxFileBytes+1); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		_, _, err := Build([]string{p}, "x")
		if err == nil {
			t.Fatal("expected error for oversized file")
		}
		if !strings.Contains(err.Error(), "too large") {
			t.Errorf("error = %q, want it to mention 'too large'", err.Error())
		}
	})

	t.Run("no attachments", func(t *testing.T) {
		if _, _, err := Build(nil, "prompt only"); err == nil {
			t.Fatal("expected error when no paths given")
		}
	})

	t.Run("directory", func(t *testing.T) {
		if _, _, err := Build([]string{dir}, "x"); err == nil {
			t.Fatal("expected error for a directory path")
		}
	})

	t.Run("non-regular file (fifo)", func(t *testing.T) {
		p := filepath.Join(dir, "pipe")
		if err := syscall.Mkfifo(p, 0o644); err != nil {
			t.Skipf("mkfifo unavailable: %v", err)
		}
		// A fifo reports Size()==0 (passes the cap) but ReadFile would block.
		// Build must reject it up front as non-regular, never attempt the read.
		done := make(chan error, 1)
		go func() {
			_, _, err := Build([]string{p}, "x")
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("expected error for a non-regular (fifo) file")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Build blocked on a fifo instead of rejecting it")
		}
	})
}

func TestBuild_OrderingSingleImage(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "mock.png", pngBytes)
	blocks, _, err := Build([]string{p}, "what is this")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks (image,text), got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Type != "image" {
		t.Errorf("block[0].Type = %q, want image (image before text)", blocks[0].Type)
	}
	if blocks[1].Type != "text" || blocks[1].Text != "what is this" {
		t.Errorf("block[1] = %+v, want text 'what is this'", blocks[1])
	}
}

func TestBuild_OrderingSingleImageNoPrompt(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "mock.png", pngBytes)
	blocks, _, err := Build([]string{p}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Type != "image" {
		t.Fatalf("want single image block, got %+v", blocks)
	}
}

func TestBuild_OrderingMultiImageLabels(t *testing.T) {
	dir := t.TempDir()
	p1 := writeFile(t, dir, "mock.png", pngBytes)
	p2 := writeFile(t, dir, "err.jpg", jpegBytes)
	blocks, _, err := Build([]string{p1, p2}, "compare")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// [text "[image 1: mock.png]", image, text "[image 2: err.jpg]", image, text "compare"]
	if len(blocks) != 5 {
		t.Fatalf("want 5 blocks, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Type != "text" || blocks[0].Text != "[image 1: mock.png]" {
		t.Errorf("block[0] = %+v, want label '[image 1: mock.png]'", blocks[0])
	}
	if blocks[1].Type != "image" {
		t.Errorf("block[1].Type = %q, want image", blocks[1].Type)
	}
	if blocks[2].Type != "text" || blocks[2].Text != "[image 2: err.jpg]" {
		t.Errorf("block[2] = %+v, want label '[image 2: err.jpg]'", blocks[2])
	}
	if blocks[3].Type != "image" {
		t.Errorf("block[3].Type = %q, want image", blocks[3].Type)
	}
	if blocks[4].Type != "text" || blocks[4].Text != "compare" {
		t.Errorf("block[4] = %+v, want text 'compare'", blocks[4])
	}
}

func TestBuild_MultiImageNoPromptDropsTrailingText(t *testing.T) {
	dir := t.TempDir()
	p1 := writeFile(t, dir, "a.png", pngBytes)
	p2 := writeFile(t, dir, "b.png", pngBytes)
	blocks, _, err := Build([]string{p1, p2}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// [label, image, label, image] — no trailing prompt text.
	if len(blocks) != 4 {
		t.Fatalf("want 4 blocks, got %d: %+v", len(blocks), blocks)
	}
	if blocks[len(blocks)-1].Type != "image" {
		t.Errorf("last block = %+v, want image (no empty trailing text)", blocks[len(blocks)-1])
	}
}

func TestBuild_Chips(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "mock.png", pngBytes)
	_, chips, err := Build([]string{p}, "x")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(chips) != 1 {
		t.Fatalf("want 1 chip, got %d", len(chips))
	}
	if chips[0].Name != "mock.png" {
		t.Errorf("chip name = %q, want mock.png", chips[0].Name)
	}
	if chips[0].MediaType != "image/png" {
		t.Errorf("chip media_type = %q, want image/png", chips[0].MediaType)
	}
	if chips[0].HumanSize == "" {
		t.Errorf("chip HumanSize empty")
	}
}

// Chips must track file order for a multi-image attach so the TUI chip lines
// line up with the interleaved labels.
func TestBuild_ChipsTrackFileOrder(t *testing.T) {
	dir := t.TempDir()
	p1 := writeFile(t, dir, "first.png", pngBytes)
	p2 := writeFile(t, dir, "second.jpg", jpegBytes)
	_, chips, err := Build([]string{p1, p2}, "x")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(chips) != 2 {
		t.Fatalf("want 2 chips, got %d", len(chips))
	}
	if chips[0].Name != "first.png" || chips[1].Name != "second.jpg" {
		t.Errorf("chip order = [%q,%q], want [first.png,second.jpg]", chips[0].Name, chips[1].Name)
	}
}

// G6: the guard is on the COMBINED assembled line — multiple files each under
// the per-file cap whose sum exceeds the line ceiling must be rejected. Uses
// the injectable build() seam so no multi-MB payload is allocated.
func TestBuild_G6CombinedLineGuard(t *testing.T) {
	dir := t.TempDir()
	// Two ~1KB image files, each well under the per-file cap.
	big := append([]byte(nil), pngBytes...)
	big = append(big, make([]byte, 1024)...)
	p1 := writeFile(t, dir, "a.png", big)
	p2 := writeFile(t, dir, "b.png", big)

	// maxFile generous (each file passes); maxLine between one file's encoded
	// size and the two-file sum → the summation guard must trip.
	_, _, err := build([]string{p1, p2}, "x", MaxFileBytes, 1500)
	if err == nil {
		t.Fatal("expected combined-line guard to reject two files")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q, want it to mention 'too large'", err.Error())
	}

	// A single small image under the real ceiling passes through Build (proves
	// Build wires the real MaxAssembledLineBytes, not a dead 0).
	small := writeFile(t, dir, "small.png", pngBytes)
	if _, _, err := Build([]string{small}, "x"); err != nil {
		t.Errorf("small image under real ceiling should pass Build, got %v", err)
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{6, "6 B"},
		{320 * 1024, "320 KB"},
		{1536, "1.5 KB"},
		{2*1024*1024 + 512*1024, "2.5 MB"},
	}
	for _, tt := range tests {
		if got := humanSize(tt.n); got != tt.want {
			t.Errorf("humanSize(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
