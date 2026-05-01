// flush.go is now a thin re-export of internal/inboxprompt for backwards
// compat with internal agentloop callers. The real implementation moved out
// in QUM-437 so the unified-runtime supervisor path can build identical
// inbox/interrupt prompts without importing agentloop.
package agentloop

import "github.com/dmotles/sprawl/internal/inboxprompt"

const (
	MaxQueueFlushBodyBytes  = inboxprompt.MaxQueueFlushBodyBytes
	MaxQueueFlushTotalBytes = inboxprompt.MaxQueueFlushTotalBytes
)

var (
	SplitByClass              = inboxprompt.SplitByClass
	BuildQueueFlushPrompt     = inboxprompt.BuildQueueFlushPrompt
	BuildInterruptFlushPrompt = inboxprompt.BuildInterruptFlushPrompt
)
