package agentloop

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	backend "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/rootinit"
	"github.com/dmotles/sprawl/internal/state"
)

// BuildAgentSessionSpec builds the shared backend session spec for a child agent.
func BuildAgentSessionSpec(agentState *state.AgentState, promptPath, sprawlRoot string, stderr io.Writer) backend.SessionSpec {
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
	return backend.SessionSpec{
		WorkDir:         agentState.Worktree,
		Identity:        agentState.Name,
		SprawlRoot:      sprawlRoot,
		SessionID:       agentState.SessionID,
		PromptFile:      promptPath,
		Model:           rootinit.ModelForAgentType(agentState.Type),
		Effort:          "medium",
		PermissionMode:  "bypassPermissions",
		AdditionalEnv:   additionalEnv,
		Stderr:          stderr,
		DisallowedTools: rootinit.ChildDisallowedTools,
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
