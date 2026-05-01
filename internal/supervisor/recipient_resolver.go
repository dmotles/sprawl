package supervisor

import "github.com/dmotles/sprawl/internal/messages"

// unifiedRuntimeHandle is implemented by RuntimeHandles whose wake/interrupt
// path is in-memory (currently only *unifiedHandle). The marker keeps
// messages.RecipientResolver decoupled from concrete handle types.
type unifiedRuntimeHandle interface {
	isUnifiedHandle()
}

// NewRecipientResolver returns a messages.RecipientResolver backed by the
// supervisor RuntimeRegistry. It returns:
//   - RecipientUnified iff the recipient has a started runtime whose handle
//     implements unifiedRuntimeHandle,
//   - RecipientLegacy iff the recipient has a started runtime with a non-
//     unified handle,
//   - RecipientUnknown otherwise (nil registry, registry miss, not-yet-
//     started, nil handle).
//
// Fail-open by design: any uncertainty resolves to Unknown so the caller
// keeps writing the legacy `.wake` sentinel. See QUM-438.
func NewRecipientResolver(reg *RuntimeRegistry) messages.RecipientResolver {
	return func(name string) messages.RecipientKind {
		if reg == nil {
			return messages.RecipientUnknown
		}
		rt, ok := reg.Get(name)
		if !ok || rt == nil {
			return messages.RecipientUnknown
		}
		if rt.Snapshot().Lifecycle != RuntimeLifecycleStarted {
			return messages.RecipientUnknown
		}
		h := rt.currentHandle()
		if h == nil {
			return messages.RecipientUnknown
		}
		if _, ok := h.(unifiedRuntimeHandle); ok {
			return messages.RecipientUnified
		}
		return messages.RecipientLegacy
	}
}
