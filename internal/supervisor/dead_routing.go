// QUM-725: WalkDeadAncestors — pure helper that walks the ancestor chain
// starting at `target` and stops at the first live ancestor. Used by
// Real.SendMessage and Real.ReportStatus to redirect deliveries away from
// Died agents.
package supervisor

import (
	"fmt"

	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// LivenessProbe reports the liveness of an agent by name. ok=false means the
// probe has no knowledge of that agent (neither registered runtime nor disk
// state). Implementations should be cheap and side-effect-free; callers may
// invoke this many times in a single walk.
type LivenessProbe func(name string) (liveness.AgentLiveness, bool)

// ParentLookup returns the persisted Parent of an agent by name. Empty
// string means "no parent" (root). An error is propagated up to the caller.
type ParentLookup func(name string) (string, error)

// walkDepthCap bounds the number of hops WalkDeadAncestors will follow before
// erroring. Mirrors isAncestor's depth cap so the two helpers fail together on
// pathological state files.
const walkDepthCap = 16

// WalkDeadAncestors walks the ancestor chain from `target` upward until it
// finds the first ancestor whose liveness is NOT Died. Returns:
//
//   - (liveAncestor, deadChain, nil) — `target` is Died; deadChain enumerates
//     the dead names in chain order starting at `target`. liveAncestor is the
//     first ancestor that is not Died.
//   - ("", nil, nil)                 — `target` is not Died (no rerouting
//     needed). The caller should send to `target` directly.
//   - ("", nil, error)               — depth cap exceeded, cycle detected,
//     parent lookup failed, or `target` is unknown to the probe.
func WalkDeadAncestors(target string, liv LivenessProbe, parentOf ParentLookup) (string, []string, error) {
	if target == "" {
		return "", nil, fmt.Errorf("WalkDeadAncestors: empty target")
	}
	state, ok := liv(target)
	if !ok {
		return "", nil, fmt.Errorf("WalkDeadAncestors: target %q unknown to liveness probe", target)
	}
	if state != liveness.Died {
		return "", nil, nil
	}

	var dead []string
	visited := map[string]bool{}
	current := target
	for hop := 0; hop < walkDepthCap; hop++ {
		if visited[current] {
			return "", nil, fmt.Errorf("WalkDeadAncestors: cycle detected at %q", current)
		}
		visited[current] = true
		dead = append(dead, current)

		parent, err := parentOf(current)
		if err != nil {
			return "", nil, fmt.Errorf("WalkDeadAncestors: parent lookup for %q: %w", current, err)
		}
		if parent == "" {
			return "", nil, fmt.Errorf("WalkDeadAncestors: reached root %q with no live ancestor", current)
		}
		parentLiv, ok := liv(parent)
		if !ok {
			// Unknown parent — treat as live (best we can do). The caller will
			// surface a clearer error if the parent doesn't actually exist on
			// the message-send path.
			return parent, dead, nil
		}
		if parentLiv != liveness.Died {
			return parent, dead, nil
		}
		current = parent
	}
	return "", nil, fmt.Errorf("WalkDeadAncestors: chain exceeds %d hops starting from %q", walkDepthCap, target)
}
