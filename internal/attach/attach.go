// Package attach assembles local image files into Anthropic content blocks for
// the sprawl TUI `/attach` command (QUM-860). It is deliberately free of any
// TUI or runtime dependency so the validation + block-assembly logic unit-tests
// in isolation.
//
// The host-side contract: given filesystem paths plus an optional prompt,
// validate each file (exists+readable, content-sniffed media_type, format
// allowlist, per-file size cap, combined-line cap), base64-encode it, and
// assemble image-before-text content blocks. EXIF is never stripped and image
// dimensions are never pre-checked — the API rejects oversized dimensions and
// that rejection is surfaced by the caller.
package attach

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/dmotles/sprawl/internal/protocol"
)

const (
	// MaxFileBytes is the per-file size ceiling (Claude API base64 image cap is
	// 10 MB). Checked via stat before the file is read.
	MaxFileBytes int64 = 10 << 20
	// MaxAssembledLineBytes guards the combined stream-json line (G6): base64
	// (~1.33×) plus JSON overhead for a batch of images must stay under the
	// host stdin/reader limit. The protocol reader's per-line ceiling is 100 MB
	// (protocol.DefaultMaxLineSize); 90 MB leaves headroom for envelope
	// overhead while still admitting a single ~10 MB image.
	MaxAssembledLineBytes int64 = 90 << 20
)

// allowedMediaTypes is the format allowlist. Anything http.DetectContentType
// reports outside this set is rejected.
var allowedMediaTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// Chip is the per-file display metadata the TUI renders as a text chip
// (`📎 name · media_type · size`). No image bytes — presentation only.
type Chip struct {
	Name      string
	MediaType string
	HumanSize string
}

// Build validates each path and assembles image-before-text content blocks plus
// per-file chip metadata. See package doc for the validation contract. Returns
// a clear error (and nil blocks/chips) on the first invalid file or if the
// combined assembled line would exceed the cap.
func Build(paths []string, prompt string) ([]protocol.ContentBlock, []Chip, error) {
	return build(paths, prompt, MaxFileBytes, MaxAssembledLineBytes)
}

// build is the injectable core of Build, parameterized on the size caps so the
// per-file and combined-line guards are unit-testable without allocating
// multi-megabyte payloads.
func build(paths []string, prompt string, maxFile, maxLine int64) ([]protocol.ContentBlock, []Chip, error) {
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("no attachments given")
	}

	type loaded struct {
		name      string
		mediaType string
		data      string // base64
		size      int64
	}
	imgs := make([]loaded, 0, len(paths))
	var totalEncoded int64
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot read attachment %q: %w", p, err)
		}
		// Reject non-regular files up front: a directory, fifo, device, or
		// socket reports Size()==0 (passing the cap) but a read would fail,
		// block, or grow unbounded. Only real files carry image bytes.
		if !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("cannot read attachment %q: not a regular file", p)
		}
		if info.Size() > maxFile {
			return nil, nil, fmt.Errorf("attachment %q is too large (%s, max %s)", p, humanSize(info.Size()), humanSize(maxFile))
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot read attachment %q: %w", p, err)
		}
		mediaType := sniffMediaType(raw)
		if !allowedMediaTypes[mediaType] {
			return nil, nil, fmt.Errorf("unsupported format: %s (%q) — only jpeg, png, gif, webp are supported", mediaType, filepath.Base(p))
		}
		encoded := base64.StdEncoding.EncodeToString(raw)
		totalEncoded += int64(len(encoded))
		imgs = append(imgs, loaded{
			name:      filepath.Base(p),
			mediaType: mediaType,
			data:      encoded,
			size:      info.Size(),
		})
	}

	if totalEncoded > maxLine {
		return nil, nil, fmt.Errorf("combined attachments too large (%s encoded, max %s per message)", humanSize(totalEncoded), humanSize(maxLine))
	}

	blocks := make([]protocol.ContentBlock, 0, len(imgs)*2+1)
	chips := make([]Chip, 0, len(imgs))
	multi := len(imgs) > 1
	for i, im := range imgs {
		if multi {
			// Interleave a short label before each image so the model can refer
			// to them by index/name.
			blocks = append(blocks, protocol.ContentBlock{
				Type: "text",
				Text: fmt.Sprintf("[image %d: %s]", i+1, im.name),
			})
		}
		blocks = append(blocks, protocol.ContentBlock{
			Type: "image",
			Source: &protocol.ImageSource{
				Type:      "base64",
				MediaType: im.mediaType,
				Data:      im.data,
			},
		})
		chips = append(chips, Chip{
			Name:      im.name,
			MediaType: im.mediaType,
			HumanSize: humanSize(im.size),
		})
	}
	// Prompt text comes AFTER the image(s) (vision docs: image-before-text).
	if prompt != "" {
		blocks = append(blocks, protocol.ContentBlock{Type: "text", Text: prompt})
	}
	return blocks, chips, nil
}

// sniffMediaType returns the MIME type of the leading bytes via
// http.DetectContentType (magic bytes), ignoring any filename extension. The
// stripped-of-params form is returned (e.g. "image/png").
func sniffMediaType(data []byte) string {
	ct := http.DetectContentType(data)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

// humanSize renders a byte count as a compact human-readable string using
// 1024-based units. Whole values drop the decimal ("320 KB"); fractional values
// keep one decimal ("1.5 KB", "2.5 MB").
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	switch {
	case n < unit*unit:
		return fmtUnit(float64(n)/unit, "KB")
	case n < unit*unit*unit:
		return fmtUnit(float64(n)/(unit*unit), "MB")
	default:
		return fmtUnit(float64(n)/(unit*unit*unit), "GB")
	}
}

func fmtUnit(v float64, unit string) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d %s", int64(v), unit)
	}
	return fmt.Sprintf("%.1f %s", v, unit)
}

// ParseArgs splits a `/attach` argument line into file paths and a prompt.
// Double-quoted segments (space-joined if more than one) form the prompt;
// every unquoted whitespace-delimited token is a path. Tilde/glob expansion is
// intentionally NOT performed — paths are taken literally and resolved by the
// filesystem at Build time. An unterminated quote takes the remainder of the
// line as prompt text.
func ParseArgs(raw string) (paths []string, prompt string) {
	var prompts []string
	i := 0
	n := len(raw)
	for i < n {
		// Skip whitespace between tokens.
		if raw[i] == ' ' || raw[i] == '\t' {
			i++
			continue
		}
		if raw[i] == '"' {
			i++ // past opening quote
			start := i
			for i < n && raw[i] != '"' {
				i++
			}
			prompts = append(prompts, raw[start:i])
			if i < n {
				i++ // past closing quote
			}
			continue
		}
		// Unquoted token → path.
		start := i
		for i < n && raw[i] != ' ' && raw[i] != '\t' {
			i++
		}
		paths = append(paths, raw[start:i])
	}
	return paths, strings.Join(prompts, " ")
}
