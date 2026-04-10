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
	// The status-right value is wrapped in outer quotes. If the outer quotes are
	// single quotes, any single quotes inside #() shell commands (tr -d ' ',
	// display-message -p '#{window_name}', echo '...') would prematurely terminate
	// the outer value, causing tmux parse errors.
	for _, line := range strings.Split(cfg, "\n") {
		if !strings.HasPrefix(line, "set -g status-right ") {
			continue
		}
		// If using outer single quotes, count them — should only be 0 (using double quotes)
		// or exactly 2 (open+close with no inner singles). More than 2 means nested conflict.
		value := strings.TrimPrefix(line, "set -g status-right ")
		if len(value) > 0 && value[0] == '\'' {
			// Outer delimiter is single quote — check for nested singles
			singleCount := strings.Count(value, "'")
			if singleCount > 2 {
				t.Errorf("status-right has %d single quotes — nested single quotes inside outer single-quoted value will break tmux parser", singleCount)
			}
		}
		return
	}
	t.Error("no 'set -g status-right' line found in generated config")
}

func TestGenerateConfig_StatusRightUsesDoubleQuotes(t *testing.T) {
	cfg := GenerateConfig(ConfigParams{
		AccentColor: "colour39",
		Namespace:   "⚡",
		Version:     "0.1.3",
		SprawlRoot:  "/home/user/myproject",
	})
	// The status-right line must use double quotes as the outer value delimiter.
	// Inner #() shell commands use single quotes (e.g. tr -d ' ', display-message -p '#{window_name}'),
	// so using outer single quotes would cause nested quote syntax errors in tmux.
	for _, line := range strings.Split(cfg, "\n") {
		if !strings.HasPrefix(line, "set -g status-right ") {
			continue
		}
		// Should be: set -g status-right "..."
		if strings.Contains(line, "status-right '") {
			t.Error("status-right must use double quotes as outer delimiter, not single quotes — inner #() shell commands use single quotes")
		}
		if !strings.Contains(line, `status-right "`) {
			t.Error("status-right should use double quotes as outer value delimiter")
		}
		return
	}
	t.Error("no status-right line found in generated config")
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
	if !strings.Contains(cfg, `tmux display-message -p '#{window_name}'`) {
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
