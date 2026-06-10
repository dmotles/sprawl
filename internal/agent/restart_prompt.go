// Package agent (extension) — QUM-723.
package agent

// RestartInjectionPrompt is the canonical neutral prompt injected as the first
// post-resume turn after `sprawl enter` restarts and Real.RecoverAgents brings
// previously-live child agents back. Centralized here so the wording is
// reviewed in one place. Edit with care: a unit test pins this byte-for-byte
// against the spec literal (see QUM-723).
const RestartInjectionPrompt = "Sprawl was just restarted. If you were in the middle of something, continue where you left off. Otherwise, hang tight."
