// flush.go is now a thin re-export of internal/inboxprompt for backwards
// compat with internal agentloop callers. The real implementation moved out
// in QUM-437 so the unified-runtime supervisor path can build identical
// inbox/interrupt prompts without importing agentloop. QUM-555 slimmed the
// frames to a one-line `<system-notification>` shape and removed the size
// caps that were only meaningful while bodies were inlined.
package agentloop

import "github.com/dmotles/sprawl/internal/inboxprompt"

var (
	SplitByClass              = inboxprompt.SplitByClass
	BuildQueueFlushPrompt     = inboxprompt.BuildQueueFlushPrompt
	BuildInterruptFlushPrompt = inboxprompt.BuildInterruptFlushPrompt
)
