package memory

import "time"

// TimelineCompressionConfig is retained as the public dependency-injection
// shape for Consolidate. Most fields from the old, multi-step compression
// pipeline are gone (QUM-517 cutover); only Model and InvokeTimeout are
// still honored. The struct is kept (rather than replaced) so existing
// rootinit wiring + tests do not have to be rewritten.
type TimelineCompressionConfig struct {
	// Model is the Claude model name passed to the invoker. Empty means
	// "let the claude CLI pick" (typically the user's default).
	Model string
	// InvokeTimeout bounds the per-session claude -p call. Zero falls
	// back to DefaultInvokeTimeout.
	InvokeTimeout time.Duration
}

// DefaultTimelineCompressionConfig returns a TimelineCompressionConfig with
// sensible defaults.
func DefaultTimelineCompressionConfig() TimelineCompressionConfig {
	return TimelineCompressionConfig{
		Model:         DefaultMemoryModel,
		InvokeTimeout: DefaultInvokeTimeout,
	}
}

// DefaultMemoryModel is the Claude model used for memory distillation
// (consolidation + persistent-knowledge updates). Distillation is a
// structured bullet-extraction task — sonnet handles it well without the
// latency/cost of opus. Users can override via .sprawl/config.yaml
// `memory_model` or by constructing a non-default config.
const DefaultMemoryModel = "sonnet"

// DefaultInvokeTimeout bounds each individual claude -p call made by the
// memory pipeline. Picked to cover slow-prompt + slow-network worst cases
// while still recovering from genuinely stuck invocations.
const DefaultInvokeTimeout = 120 * time.Second
