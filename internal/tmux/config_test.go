package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateConfig_ContainsMouseOn(t *testing.T) {
	cfg := GenerateConfig(ConfigParams{
		AccentColor: "colour39",
		Namespace:   "⚡",
		Version:     "0.1.3",
		SprawlRoot:  "/tmp/test",
	})
	if !strings.Contains(cfg, "mouse on") {
		t.Error("config should contain 'mouse on'")
	}
}

func TestGenerateConfig_ContainsHistoryLimit(t *testing.T) {
	cfg := GenerateConfig(ConfigParams{
		AccentColor: "colour39",
		Namespace:   "⚡",
		Version:     "0.1.3",
		SprawlRoot:  "/tmp/test",
	})
	if !strings.Contains(cfg, "history-limit 50000") {
		t.Error("config should contain 'history-limit 50000'")
	}
}

func TestGenerateConfig_ContainsStatusInterval(t *testing.T) {
	cfg := GenerateConfig(ConfigParams{
		AccentColor: "colour39",
		Namespace:   "⚡",
		Version:     "0.1.3",
		SprawlRoot:  "/tmp/test",
	})
	if !strings.Contains(cfg, "status-interval 5") {
		t.Error("config should contain 'status-interval 5'")
	}
}

func TestGenerateConfig_ContainsAccentColor(t *testing.T) {
	cfg := GenerateConfig(ConfigParams{
		AccentColor: "colour198",
		Namespace:   "⚡",
		Version:     "0.1.3",
		SprawlRoot:  "/tmp/test",
	})
	if !strings.Contains(cfg, "colour198") {
		t.Error("config should contain the accent color 'colour198'")
	}
}

func TestGenerateConfig_ContainsNamespace(t *testing.T) {
	cfg := GenerateConfig(ConfigParams{
		AccentColor: "colour39",
		Namespace:   "🔮",
		Version:     "0.1.3",
		SprawlRoot:  "/tmp/test",
	})
	if !strings.Contains(cfg, "🔮") {
		t.Error("config should contain the namespace emoji")
	}
}

func TestGenerateConfig_ContainsBranding(t *testing.T) {
	cfg := GenerateConfig(ConfigParams{
		AccentColor: "colour39",
		Namespace:   "⚡",
		Version:     "0.1.3",
		SprawlRoot:  "/tmp/test",
	})
	if !strings.Contains(cfg, "S P R A W L") {
		t.Error("config should contain letterspaced 'S P R A W L' branding")
	}
}

func TestGenerateConfig_ContainsVersion(t *testing.T) {
	cfg := GenerateConfig(ConfigParams{
		AccentColor: "colour39",
		Namespace:   "⚡",
		Version:     "0.1.3",
		SprawlRoot:  "/tmp/test",
	})
	if !strings.Contains(cfg, "0.1.3") {
		t.Error("config should contain the version string")
	}
}

func TestGenerateConfig_NoKeybindings(t *testing.T) {
	cfg := GenerateConfig(ConfigParams{
		AccentColor: "colour39",
		Namespace:   "⚡",
		Version:     "0.1.3",
		SprawlRoot:  "/tmp/test",
	})
	if strings.Contains(cfg, "bind-key") || strings.Contains(cfg, "bind ") {
		t.Error("config should not contain any keybindings")
	}
}

func TestGenerateConfig_UsesSprawlRootForDynamicContent(t *testing.T) {
	cfg := GenerateConfig(ConfigParams{
		AccentColor: "colour39",
		Namespace:   "⚡",
		Version:     "0.1.3",
		SprawlRoot:  "/home/user/myproject",
	})
	// Root path should appear double-quoted (not single-quoted) inside #() commands
	// to avoid nested single-quote conflicts with the outer set -g '...' value.
	if !strings.Contains(cfg, `"/home/user/myproject"`) {
		t.Error("config should reference SPRAWL_ROOT with double quotes for #() shell commands")
	}
}

func TestGenerateConfig_NoNestedSingleQuotesInStatusRight(t *testing.T) {
	cfg := GenerateConfig(ConfigParams{
		AccentColor: "colour39",
		Namespace:   "⚡",
		Version:     "0.1.3",
		SprawlRoot:  "/home/user/myproject",
	})
	// Extract the status-right line and verify it doesn't have nested single quotes.
	// The outer set -g status-right '...' value should not contain unescaped single
	// quotes from ShellQuote inside #() expansions.
	for _, line := range strings.Split(cfg, "\n") {
		if !strings.Contains(line, "status-right") {
			continue
		}
		// Find the content between the outer single quotes of set -g status-right '...'
		idx := strings.Index(line, "status-right '")
		if idx == -1 {
			continue
		}
		inner := line[idx+len("status-right '"):]
		// The inner content should not contain single-quoted paths like '/path/to/root'
		if strings.Contains(inner, "'/home/user/myproject'") {
			t.Error("status-right should not contain single-quoted paths inside #() commands — causes nested quote syntax errors in tmux")
		}
	}
}

func TestGenerateConfig_UsesWindowNameForIdentity(t *testing.T) {
	cfg := GenerateConfig(ConfigParams{
		AccentColor: "colour39",
		Namespace:   "⚡",
		Version:     "0.1.3",
		SprawlRoot:  "/tmp/test",
	})
	// Should use #W (tmux window name format var) for agent identity in the status-left,
	// not $SPRAWL_AGENT_IDENTITY which is session-scoped and wrong for child sessions.
	if !strings.Contains(cfg, "#W") {
		t.Error("config should use #W (tmux window name) for agent identity display")
	}
}

func TestGenerateConfig_MailCountUsesWindowName(t *testing.T) {
	cfg := GenerateConfig(ConfigParams{
		AccentColor: "colour39",
		Namespace:   "⚡",
		Version:     "0.1.3",
		SprawlRoot:  "/tmp/test",
	})
	// Mail count should use tmux display-message to resolve window name dynamically,
	// not $SPRAWL_AGENT_IDENTITY which is session-scoped.
	if !strings.Contains(cfg, "tmux display-message -p '#{window_name}'") {
		t.Error("config mail count should use tmux display-message to get window name")
	}
}

func TestWriteConfig_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	params := ConfigParams{
		AccentColor: "colour39",
		Namespace:   "⚡",
		Version:     "0.1.3",
		SprawlRoot:  dir,
	}

	path, err := WriteConfig(params)
	if err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	expectedPath := filepath.Join(dir, ".sprawl", "tmux.conf")
	if path != expectedPath {
		t.Errorf("path = %q, want %q", path, expectedPath)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading config file: %v", err)
	}

	expected := GenerateConfig(params)
	if string(data) != expected {
		t.Error("file content should match GenerateConfig output")
	}
}

func TestPickAccentColor_ReturnsFromPool(t *testing.T) {
	color := PickAccentColor()
	found := false
	for _, c := range AccentColorPool {
		if c == color {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("PickAccentColor returned %q, which is not in AccentColorPool", color)
	}
}

func TestAccentColorPool_HasEnoughColors(t *testing.T) {
	if len(AccentColorPool) < 8 {
		t.Errorf("AccentColorPool has %d colors, want at least 8", len(AccentColorPool))
	}
}
