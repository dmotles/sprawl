package agentloop

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	backend "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/card"
	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/rootinit"
	"github.com/dmotles/sprawl/internal/state"
)

// BuildAgentSessionSpec builds the shared backend session spec for a child agent.
//
// c is the agent card the launch resolved (QUM-1251, M2) and may be nil, which
// means "no card — use the per-type defaults". The card is passed IN rather than
// resolved here so the same pointer governs both the model and the system
// prompt: resolving twice would let a spawn render one card's prompt while
// launching another card's model, and nothing about the result would say so.
func BuildAgentSessionSpec(agentState *state.AgentState, promptPath, sprawlRoot string, stderr io.Writer, c *card.Card) backend.SessionSpec {
	additionalEnv := map[string]string{}
	if agentState.TreePath != "" {
		additionalEnv["SPRAWL_TREE_PATH"] = agentState.TreePath
	}
	if namespace := state.ReadNamespace(sprawlRoot); namespace != "" {
		additionalEnv["SPRAWL_NAMESPACE"] = namespace
	}
	if sprawlBin := os.Getenv("SPRAWL_BIN"); sprawlBin != "" {
		additionalEnv["SPRAWL_BIN"] = sprawlBin
	}
	if testMode := os.Getenv("SPRAWL_TEST_MODE"); testMode != "" {
		additionalEnv["SPRAWL_TEST_MODE"] = testMode
	}
	// Model precedence, widest default first:
	//
	//  1. rootinit.ModelForAgentType — the compiled-in per-type default, and
	//     still the floor when there is no card (the `sprawl def` path can be
	//     off, and a resolver failure must not change which model launches).
	//  2. the CARD (QUM-1251) — the published definition of the agent type.
	//     This is what AC2 is about: change a card's model, and the model the
	//     child launches with changes, with no binary rebuild.
	//  3. AgentState.Model — an explicit per-agent override, validated at spawn
	//     time. QUM-851, and it stays the strongest: an operator who named a
	//     model for one agent is not overruled by a card edit.
	//
	// Effort follows the card the same way, defaulting to "low" (QUM-1276: every
	// child launches at low). After this change that invariant is a property of
	// the SEEDS rather than of Go — see TestSeeds_AgreeWithTheCompiledInDefaults.
	model := rootinit.ModelForAgentType(agentState.Type)
	effort := "low"
	if c != nil {
		if c.Model != "" {
			model = c.Model
		}
		if c.Effort != "" {
			effort = c.Effort
		}
	}
	if agentState.Model != "" {
		model = agentState.Model
	}
	return backend.SessionSpec{
		WorkDir:         agentState.Worktree,
		Identity:        agentState.Name,
		SprawlRoot:      sprawlRoot,
		SessionID:       agentState.SessionID,
		PromptFile:      promptPath,
		Model:           model,
		Effort:          effort,
		PermissionMode:  "bypassPermissions",
		AdditionalEnv:   additionalEnv,
		Stderr:          stderr,
		DisallowedTools: rootinit.ChildDisallowedTools,
		// QUM-817: the CLI must echo each consumed stdin user message back on
		// stdout (isReplay) so the runtime can confirm consumption — the ack
		// that drives MarkDelivered, task completion, and no-reinjection.
		ReplayUserMessages: true,
	}
}

// ObserverWriter renders protocol events to the child runtime transcript/log.
type ObserverWriter struct {
	W    io.Writer
	Ring *ActivityRing
}

func (t *ObserverWriter) OnMessage(msg *protocol.Message) {
	if t.Ring != nil {
		t.Ring.RecordMessage(msg, time.Now)
	}
	switch msg.Type {
	case "assistant":
		var outer struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					Name  string          `json:"name,omitempty"`
					Input json.RawMessage `json:"input,omitempty"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(msg.Raw, &outer); err != nil {
			return
		}
		for _, block := range outer.Message.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					fmt.Fprintf(t.W, "[claude] %s\n", block.Text)
				}
			case "tool_use":
				if block.Name != "" {
					inputStr := string(block.Input)
					if runes := []rune(inputStr); len(runes) > 200 {
						inputStr = string(runes[:197]) + "..."
					}
					fmt.Fprintf(t.W, "[tool] %s: %s\n", block.Name, inputStr)
				}
			}
		}

	case "system":
		if msg.Subtype == "session_state_changed" {
			var ssc protocol.SessionStateChanged
			if err := json.Unmarshal(msg.Raw, &ssc); err == nil && ssc.State != "" {
				fmt.Fprintf(t.W, "[system] %s: %s\n", msg.Subtype, ssc.State)
				return
			}
		}
		if msg.Subtype != "" {
			fmt.Fprintf(t.W, "[system] %s\n", msg.Subtype)
		}

	case "result":
		var res protocol.ResultMessage
		if err := json.Unmarshal(msg.Raw, &res); err != nil {
			return
		}
		status := "success"
		if res.IsError {
			status = "error"
		}
		fmt.Fprintf(t.W, "[result] %s (stop=%s, turns=%d)\n", status, res.StopReason, res.NumTurns)

	case "rate_limit_event":
		var evt protocol.RateLimitEvent
		if err := json.Unmarshal(msg.Raw, &evt); err != nil {
			return
		}
		if evt.RateLimitInfo != nil && evt.RateLimitInfo.Status == "blocked" {
			fmt.Fprintf(t.W, "[agent-loop] rate limit blocked (type=%s)\n", evt.RateLimitInfo.RateLimitType)
		}

	case "user":
	default:
		fmt.Fprintf(t.W, "[agent-loop] message: type=%s subtype=%s\n", msg.Type, msg.Subtype)
	}
}
