// Compile-time assertion that *TUIAdapter satisfies tui.BridgeDelegate.
// Lives in this package so the dependency direction stays
// tuiruntime -> tui (the legacy reverse already exists for
// MapProtocolMessage). See QUM-399.

package tuiruntime

import tui "github.com/dmotles/sprawl/internal/tui"

var _ tui.BridgeDelegate = (*TUIAdapter)(nil)
