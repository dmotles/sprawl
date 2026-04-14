package tui

import (
	"strings"
	"testing"
)

func newTestRenderer(t *testing.T) *MarkdownRenderer {
	t.Helper()
	r := NewMarkdownRenderer(80)
	if r == nil {
		t.Fatal("NewMarkdownRenderer returned nil")
	}
	return r
}

func TestNewMarkdownRenderer_NonNil(t *testing.T) {
	r := NewMarkdownRenderer(80)
	if r == nil {
		t.Fatal("NewMarkdownRenderer(80) returned nil")
	}
}

func TestMarkdownRenderer_HeadingRender(t *testing.T) {
	r := newTestRenderer(t)
	out := r.Render("# Hello World")
	plain := stripANSI(out)
	if !strings.Contains(plain, "Hello World") {
		t.Errorf("heading render should contain 'Hello World', got:\n%s", plain)
	}
}

func TestMarkdownRenderer_BoldAndItalic(t *testing.T) {
	r := newTestRenderer(t)
	out := r.Render("This is **bold** and *italic* text")
	plain := stripANSI(out)
	if !strings.Contains(plain, "bold") {
		t.Errorf("render should contain 'bold', got:\n%s", plain)
	}
	if !strings.Contains(plain, "italic") {
		t.Errorf("render should contain 'italic', got:\n%s", plain)
	}
}

func TestMarkdownRenderer_CodeBlock(t *testing.T) {
	r := newTestRenderer(t)
	input := "```go\nfmt.Println(\"hello\")\n```"
	out := r.Render(input)
	plain := stripANSI(out)
	if !strings.Contains(plain, "fmt.Println") {
		t.Errorf("code block render should contain 'fmt.Println', got:\n%s", plain)
	}
}

func TestMarkdownRenderer_InlineCode(t *testing.T) {
	r := newTestRenderer(t)
	out := r.Render("Use the `foo` command")
	plain := stripANSI(out)
	if !strings.Contains(plain, "foo") {
		t.Errorf("inline code render should contain 'foo', got:\n%s", plain)
	}
}

func TestMarkdownRenderer_List(t *testing.T) {
	r := newTestRenderer(t)
	input := "- item alpha\n- item beta\n- item gamma"
	out := r.Render(input)
	plain := stripANSI(out)
	if !strings.Contains(plain, "item alpha") {
		t.Errorf("list render should contain 'item alpha', got:\n%s", plain)
	}
	if !strings.Contains(plain, "item beta") {
		t.Errorf("list render should contain 'item beta', got:\n%s", plain)
	}
}

func TestMarkdownRenderer_Link(t *testing.T) {
	r := newTestRenderer(t)
	out := r.Render("Visit [Example Site](https://example.com) for details")
	plain := stripANSI(out)
	if !strings.Contains(plain, "Example Site") {
		t.Errorf("link render should contain 'Example Site', got:\n%s", plain)
	}
}

func TestMarkdownRenderer_UnclosedCodeFence(t *testing.T) {
	r := newTestRenderer(t)
	input := "```python\nprint('hello')\n# still typing..."
	out := r.Render(input)
	plain := stripANSI(out)
	if !strings.Contains(plain, "print") {
		t.Errorf("unclosed code fence render should contain 'print', got:\n%s", plain)
	}
}

func TestMarkdownRenderer_EmptyString(t *testing.T) {
	r := newTestRenderer(t)
	out := r.Render("")
	_ = out
}

func TestMarkdownRenderer_SetWidth(t *testing.T) {
	r := newTestRenderer(t)
	r.SetWidth(40)
	r.SetWidth(120)
	out := r.Render("After width change")
	plain := stripANSI(out)
	if !strings.Contains(plain, "After width change") {
		t.Errorf("render after SetWidth should contain text, got:\n%s", plain)
	}
}

func TestMarkdownRenderer_Table(t *testing.T) {
	r := newTestRenderer(t)
	input := "| Name | Value |\n|------|-------|\n| foo  | 42    |\n| bar  | 99    |"
	out := r.Render(input)
	plain := stripANSI(out)
	if !strings.Contains(plain, "foo") {
		t.Errorf("table render should contain 'foo', got:\n%s", plain)
	}
	if !strings.Contains(plain, "42") {
		t.Errorf("table render should contain '42', got:\n%s", plain)
	}
}
