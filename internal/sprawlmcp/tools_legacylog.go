// tools_legacylog.go — recording prose `spawn` / `retire` in the event log
// (QUM-1252, M3a slice 10).
//
// This is the whole wiring for the paired `legacy: true` lifecycle events, and
// it lives at the MCP tool layer on purpose. spawn and retire have no CLI
// command: `Server.toolSpawn` / `Server.toolRetire` are the ONLY callers of
// supervisor.Spawn and supervisor.Retire, so this seam is complete. Putting it
// one layer down in internal/supervisor would cover no additional path and would
// pull that package's large e2e-matrix row union into a diff that does not need
// it.
package sprawlmcp

import (
	"context"
	"log/slog"

	"github.com/dmotles/sprawl/internal/store"
)

// recordLegacySpawn records a prose-spawned agent's existence, best-effort.
//
// BEST-EFFORT IS THE CONTRACT, not a shortcut. The standing requirement is that
// agents never brick on the store: the agent already exists and is running by
// the time this is called, so returning an error here would report a successful
// spawn as a failure and invite the caller to spawn it again. A disabled store
// is not even logged — it is the default configuration, and warning on it would
// make every spawn on an ordinary host print a line about a feature nobody
// enabled.
func (s *Server) recordLegacySpawn(ctx context.Context, a store.LegacyAgent) {
	if s.goals == nil || !s.goals.Enabled() {
		return
	}
	if _, err := s.goals.RecordLegacySpawn(ctx, a); err != nil {
		slog.Warn("mcp.legacy_spawn_not_recorded",
			"agent", a.AgentName,
			"err", err,
			"consequence", "sprawl goals will not list this agent's work")
	}
}

// recordLegacyRetire closes the contracts for every agent a retire actually
// removed, best-effort.
//
// It takes the LIST the supervisor returned rather than the requested target: a
// cascading retire removes descendants too, and closing only the target would
// leave every descendant outstanding forever — a contract nobody can ever close,
// because the agent it names no longer exists.
func (s *Server) recordLegacyRetire(ctx context.Context, retired []string, merged bool) {
	if s.goals == nil || !s.goals.Enabled() {
		return
	}
	for _, name := range retired {
		if _, err := s.goals.RecordLegacyRetire(ctx, name, "retired", merged); err != nil {
			slog.Warn("mcp.legacy_retire_not_recorded",
				"agent", name,
				"err", err,
				"consequence", "sprawl goals will keep listing this agent as outstanding")
		}
	}
}
