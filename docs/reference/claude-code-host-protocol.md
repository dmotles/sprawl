# Claude Code Agent SDK Host Protocol

This document is a standalone implementation guide for building an Agent SDK host in Go.

Assume the reader does not have access to the Claude Code source tree, the Python SDK, or the TypeScript SDK. Everything needed to understand the protocol and implement the host is described here directly.

The goal is to explain:

- what Claude Code expects from an SDK host
- what messages flow between Claude Code and the host
- how SDK-managed MCP servers are exposed to Claude Code
- how requests, responses, cancellation, and reverse traffic work
- how to structure a Go implementation

This document focuses on the external-user SDK/headless path, not Anthropic-internal modes.

## 1. Mental Model

Claude Code is still the agent runtime.

The SDK host is a wrapper and protocol bridge around Claude Code. The host is responsible for:

1. starting Claude Code in headless streaming mode
2. sending user input into Claude Code
3. reading Claude Code output
4. handling host-side control callbacks
5. bridging any in-process MCP servers owned by the host

There are two protocol layers:

- outer layer: Claude Code control protocol
- inner layer: raw MCP JSON-RPC messages

If you miss that distinction, the implementation will be wrong.

## 2. Transport Model

The common host setup is:

- spawn Claude Code as a subprocess
- write newline-delimited JSON to its stdin
- read newline-delimited JSON from its stdout

Typical launch flags:

```text
claude \
  --print \
  --output-format stream-json \
  --input-format stream-json \
  --verbose
```

For SDK-driven sessions, treat the subprocess as a long-lived bidirectional message stream, not a one-shot CLI.

Do not assume:

1. you can write one prompt
2. close stdin immediately
3. just consume stdout

That breaks as soon as Claude Code needs a callback from the host.

## 3. What Flows Over the Stream

Messages are newline-delimited JSON objects.

You should think of the stream as carrying five broad categories of messages:

1. normal user messages from host to Claude Code
2. normal assistant/system/result messages from Claude Code to host
3. `control_request`
4. `control_response`
5. `control_cancel_request`

There are also utility messages like `keep_alive` and, from host to Claude Code, `update_environment_variables`.

The stream is bidirectional, but not perfectly symmetric:

- Claude Code may emit `control_cancel_request`
- the host should not rely on being able to send `control_cancel_request` back unless it is intentionally implementing a matching protocol extension

For a Go host, the safe rule is:

- be prepared to receive `control_cancel_request`
- do not make your design depend on sending one unless you explicitly need that behavior

## 4. Core Envelopes

### 4.1 Control request

A control request always looks like this:

```json
{
  "type": "control_request",
  "request_id": "req_123",
  "request": {
    "subtype": "initialize"
  }
}
```

Fields:

- `type`: always `"control_request"`
- `request_id`: unique per request
- `request`: subtype-specific payload

### 4.2 Control response

A successful response looks like:

```json
{
  "type": "control_response",
  "response": {
    "subtype": "success",
    "request_id": "req_123",
    "response": {}
  }
}
```

An error response looks like:

```json
{
  "type": "control_response",
  "response": {
    "subtype": "error",
    "request_id": "req_123",
    "error": "something failed"
  }
}
```

Notes:

- `request_id` always matches the original request
- error payload is a string, not an object
- some success responses have no useful `response` body

### 4.3 Cancellation

Cancellation is sent separately:

```json
{
  "type": "control_cancel_request",
  "request_id": "req_123"
}
```

Meaning:

- the side that originally sent `req_123` no longer cares about the result
- the receiver should cancel the in-flight work if possible
- if cancellation wins the race, the receiver should avoid sending a normal success reply for that request

Treat cancellation as best-effort and race-safe.

### 4.4 Keep-alive

```json
{
  "type": "keep_alive"
}
```

You can ignore this for most host implementations.

### 4.5 Update environment variables

Host-to-Claude-Code utility message:

```json
{
  "type": "update_environment_variables",
  "variables": {
    "FOO": "bar"
  }
}
```

This is not central to the SDK MCP bridge, but it is part of the stream model.

## 5. Session Bring-Up

The normal startup sequence is:

1. spawn Claude Code in `stream-json` mode
2. start a stdout reader before writing anything
3. optionally send `initialize`
4. start sending user messages
5. keep handling control traffic for the whole session

### Important nuance: `initialize` is normal, but not strictly mandatory

Most SDK hosts send an explicit `initialize` request before the first user message.

That is the recommended path because it lets the host tell Claude Code about:

- hooks
- SDK-managed MCP servers
- custom system prompt configuration
- agent metadata
- prompt-suggestion options

However, Claude Code can also start from the first user message and implicitly initialize itself.

For a Go implementation, the recommendation is simple:

- always send explicit `initialize`

That removes ambiguity and makes behavior easier to reason about.

## 6. Initialize Request

A realistic initialize request looks like:

```json
{
  "type": "control_request",
  "request_id": "init_1",
  "request": {
    "subtype": "initialize",
    "hooks": {
      "PreToolUse": [
        {
          "matcher": "Bash",
          "hookCallbackIds": ["hook_0"],
          "timeout": 30000
        }
      ]
    },
    "sdkMcpServers": ["repo_tools", "jira"],
    "jsonSchema": {
      "type": "object"
    },
    "systemPrompt": "You are operating in a Go codebase.",
    "appendSystemPrompt": "Prefer deterministic behavior.",
    "agents": {
      "reviewer": {
        "description": "Performs code review"
      }
    },
    "promptSuggestions": true,
    "agentProgressSummaries": true
  }
}
```

A realistic success response might look like:

```json
{
  "type": "control_response",
  "response": {
    "subtype": "success",
    "request_id": "init_1",
    "response": {
      "commands": [],
      "agents": [],
      "output_style": "default",
      "available_output_styles": ["default"],
      "models": [],
      "account": {
        "email": "user@example.com"
      }
    }
  }
}
```

You usually do not need to deeply interpret the initialize response to build the host. It is mostly useful as confirmation that the session is ready.

## 7. User Messages

After initialization, the host sends user input as normal stream messages.

A typical user message:

```json
{
  "type": "user",
  "session_id": "",
  "message": {
    "role": "user",
    "content": "Find dead code in this repository"
  },
  "parent_tool_use_id": null
}
```

Claude Code will then emit normal stream messages such as:

- assistant messages
- system messages
- tool summaries
- final result messages

Your stdout reader must continue handling those and control messages at the same time.

## 8. SDK-Managed MCP Servers

This is the most important part of the protocol.

An SDK-managed MCP server is a server that lives inside the host process, not as a separate stdio or HTTP server.

Examples:

- an in-process tool server created by the SDK
- a custom Go MCP server embedded in your host
- a server that exposes local business logic without spawning a child process

Claude Code needs to know two things:

1. the server exists
2. the host will transport MCP JSON-RPC for it

There are two ways to tell Claude Code about an SDK-owned server:

### 8.1 Via `initialize.sdkMcpServers`

Example:

```json
{
  "subtype": "initialize",
  "sdkMcpServers": ["repo_tools"]
}
```

### 8.2 Via startup MCP config

You can also pass a startup MCP config entry that looks like:

```json
{
  "mcpServers": {
    "repo_tools": {
      "type": "sdk",
      "name": "repo_tools"
    }
  }
}
```

That config is passed to Claude Code as CLI input, usually via `--mcp-config`.

### Recommendation

For a Go host, support both conceptually, but prefer the explicit initialize path if you control the host protocol end to end.

## 9. The Key Idea: MCP Is Tunneled Through Control Messages

Claude Code does not speak directly to SDK-owned MCP servers over a socket.

Instead:

1. Claude Code creates an internal MCP client for each SDK-owned server
2. when that client wants to send JSON-RPC, Claude Code wraps it in a `control_request`
3. the host unwraps it and forwards it to the in-process MCP server
4. the host captures the MCP server's response
5. the host wraps that response in `control_response.response.mcp_response`

That is the entire bridge.

## 10. MCP Request From Claude Code to Host

Example:

```json
{
  "type": "control_request",
  "request_id": "req_200",
  "request": {
    "subtype": "mcp_message",
    "server_name": "repo_tools",
    "message": {
      "jsonrpc": "2.0",
      "id": 7,
      "method": "tools/list",
      "params": {}
    }
  }
}
```

Meaning:

- outer request ID: `req_200`
- target SDK-owned MCP server: `repo_tools`
- inner raw JSON-RPC request: `tools/list`

The host must reply:

```json
{
  "type": "control_response",
  "response": {
    "subtype": "success",
    "request_id": "req_200",
    "response": {
      "mcp_response": {
        "jsonrpc": "2.0",
        "id": 7,
        "result": {
          "tools": [
            {
              "name": "grep_repo",
              "description": "Search repository content"
            }
          ]
        }
      }
    }
  }
}
```

Rules:

1. preserve the JSON-RPC `id`
2. preserve the MCP result shape
3. return it inside `mcp_response`
4. correlate the outer envelope using `request_id`

## 11. Notification vs Request Handling

Not every MCP message has an `id`.

If the inner JSON-RPC payload has:

- `method` and `id`: it is a request expecting a response
- `method` and no `id`: it is a notification
- `result` or `error` plus `id`: it is a response

Example notification from Claude Code:

```json
{
  "type": "control_request",
  "request_id": "req_201",
  "request": {
    "subtype": "mcp_message",
    "server_name": "repo_tools",
    "message": {
      "jsonrpc": "2.0",
      "method": "notifications/initialized"
    }
  }
}
```

In that case, the host should deliver the notification to the target server and then answer the outer control request with a dummy successful MCP response.

Example:

```json
{
  "type": "control_response",
  "response": {
    "subtype": "success",
    "request_id": "req_201",
    "response": {
      "mcp_response": {
        "jsonrpc": "2.0",
        "id": 0,
        "result": {}
      }
    }
  }
}
```

The exact dummy payload is less important than the fact that the outer request must be acknowledged successfully.

## 12. Reverse MCP Traffic: Host Server Back to Claude Code

The bridge is full duplex.

An SDK-owned MCP server may emit a message back to Claude Code that is not the direct response Claude Code is currently waiting on.

Examples:

- a notification
- a server-initiated request
- an out-of-band message from the server

When that happens, the host must wrap the raw JSON-RPC message in a new outer `control_request`.

Example:

```json
{
  "type": "control_request",
  "request_id": "req_300",
  "request": {
    "subtype": "mcp_message",
    "server_name": "repo_tools",
    "message": {
      "jsonrpc": "2.0",
      "method": "notifications/message",
      "params": {
        "level": "info",
        "data": "index warmup complete"
      }
    }
  }
}
```

Claude Code will inject that message into its internal MCP client for `repo_tools`.

This is the part that many incomplete reimplementations miss. The host is not just answering Claude Code's MCP requests. It must also forward server-originated messages back into Claude Code.

## 13. Other Control Requests the Host May Receive

Besides `mcp_message`, Claude Code may send other control requests that require host-side work.

The ones you should plan for are:

- `can_use_tool`
- `hook_callback`
- `mcp_message`

Depending on the SDK implementation you are trying to match, you may also see:

- `elicitation`

### 13.1 `can_use_tool`

Claude Code asks the host to decide whether a tool may run.

Example:

```json
{
  "type": "control_request",
  "request_id": "req_perm_1",
  "request": {
    "subtype": "can_use_tool",
    "tool_name": "Bash",
    "input": {
      "command": "git push"
    },
    "tool_use_id": "toolu_123"
  }
}
```

Possible success response:

```json
{
  "type": "control_response",
  "response": {
    "subtype": "success",
    "request_id": "req_perm_1",
    "response": {
      "behavior": "allow",
      "updatedInput": {
        "command": "git push"
      },
      "message": "Allowed by policy",
      "toolUseID": "toolu_123"
    }
  }
}
```

### 13.2 `hook_callback`

Claude Code asks the host to run a registered hook callback.

Example:

```json
{
  "type": "control_request",
  "request_id": "req_hook_1",
  "request": {
    "subtype": "hook_callback",
    "callback_id": "hook_0",
    "input": {
      "event": "PreToolUse"
    }
  }
}
```

Response shape depends on hook output, but it still uses the same outer success/error envelope.

### 13.3 `elicitation`

This is a request for user input on behalf of an MCP server.

Example:

```json
{
  "type": "control_request",
  "request_id": "req_elic_1",
  "request": {
    "subtype": "elicitation",
    "mcp_server_name": "repo_tools",
    "message": "Choose a branch",
    "mode": "form",
    "requested_schema": {
      "type": "object",
      "properties": {
        "branch": {
          "type": "string"
        }
      },
      "required": ["branch"]
    }
  }
}
```

Possible response:

```json
{
  "type": "control_response",
  "response": {
    "subtype": "success",
    "request_id": "req_elic_1",
    "response": {
      "action": "accept",
      "content": {
        "branch": "main"
      }
    }
  }
}
```

If you do not support elicitation, reply with a controlled error or explicit decline behavior depending on the compatibility target you want.

## 14. Cancellation Semantics

Suppose Claude Code sends:

```json
{
  "type": "control_request",
  "request_id": "req_500",
  "request": {
    "subtype": "hook_callback",
    "callback_id": "hook_1",
    "input": {
      "event": "PostToolUse"
    }
  }
}
```

Before your host finishes the callback, Claude Code may then send:

```json
{
  "type": "control_cancel_request",
  "request_id": "req_500"
}
```

Your host should:

1. look up the in-flight work for `req_500`
2. cancel its context
3. stop waiting on any downstream dependency if possible
4. avoid sending a normal success response if cancellation wins

Best practice:

- use `context.WithCancel` per incoming control request
- store the cancel function in a map keyed by `request_id`

## 15. Stdin Lifetime

Do not close stdin immediately after writing the initial prompt.

Why:

- Claude Code may ask for permissions after the prompt is sent
- Claude Code may need hook callback results after the prompt is sent
- Claude Code may route MCP traffic after the prompt is sent
- Claude Code may receive reverse MCP messages after the prompt is sent

The practical rule is:

- keep stdin open until you are sure no more control traffic is needed

For many single-turn hosts, that means:

1. send prompt
2. keep stdin open while awaiting the first result and any callback traffic
3. close stdin once the turn is effectively done

For interactive or long-lived sessions, keep it open for the whole session.

## 16. Recommended Go Architecture

Use three major components.

### 16.1 Claude transport

Responsible for:

- spawning Claude Code
- writing NDJSON to stdin
- reading NDJSON from stdout
- serializing writes
- closing cleanly

Suggested interface:

```go
type ClaudeTransport interface {
    Send(ctx context.Context, line []byte) error
    Recv(ctx context.Context) ([]byte, error)
    EndInput() error
    Close() error
}
```

### 16.2 Control router

Responsible for:

- parsing outer messages
- resolving `control_response`
- dispatching `control_request`
- honoring `control_cancel_request`
- forwarding ordinary assistant/result messages to the caller

Suggested core state:

```go
type PendingControl struct {
    Resp chan ControlResponse
}

type Host struct {
    transport      ClaudeTransport
    pendingControl map[string]*PendingControl
    cancelControl  map[string]context.CancelFunc
    servers        map[string]*ServerBridge
    pendingMCP     map[string]chan json.RawMessage
    mu             sync.Mutex
}
```

### 16.3 SDK MCP bridge

Responsible for:

- mapping server names to in-process MCP servers
- injecting Claude Code JSON-RPC into the right server
- capturing server-emitted JSON-RPC
- resolving pending MCP waits
- forwarding unmatched reverse traffic back to Claude Code

## 17. Recommended Go Types

You do not need these exact types, but something close helps.

```go
type ControlRequestEnvelope struct {
    Type      string          `json:"type"`
    RequestID string          `json:"request_id"`
    Request   json.RawMessage `json:"request"`
}

type ControlResponseEnvelope struct {
    Type     string          `json:"type"`
    Response json.RawMessage `json:"response"`
}

type ControlCancelEnvelope struct {
    Type      string `json:"type"`
    RequestID string `json:"request_id"`
}

type MCPMessageRequest struct {
    Subtype    string          `json:"subtype"`
    ServerName string          `json:"server_name"`
    Message    json.RawMessage `json:"message"`
}
```

For the bridge itself:

```go
type ServerBridge struct {
    Name string

    // Inject a JSON-RPC message from Claude Code into the server.
    Inject func(ctx context.Context, msg json.RawMessage) error

    // Called by the server when it emits a JSON-RPC message.
    // The host installs this callback.
    SetOutgoing func(func(ctx context.Context, msg json.RawMessage) error)
}
```

If your Go MCP library exposes a transport abstraction, adapt that instead of inventing a second custom bridge.

## 18. Main Read Loop

Your stdout reader should look roughly like this:

```go
func (h *Host) ReadLoop(ctx context.Context) error {
    for {
        line, err := h.transport.Recv(ctx)
        if err != nil {
            return err
        }

        var base struct {
            Type string `json:"type"`
        }
        if err := json.Unmarshal(line, &base); err != nil {
            continue
        }

        switch base.Type {
        case "control_response":
            if err := h.handleControlResponse(line); err != nil {
                return err
            }
        case "control_request":
            if err := h.spawnControlHandler(ctx, line); err != nil {
                return err
            }
        case "control_cancel_request":
            if err := h.handleCancel(line); err != nil {
                return err
            }
        case "keep_alive":
            continue
        default:
            h.emitSDKMessage(line)
        }
    }
}
```

## 19. Sending a Control Request to Claude Code

For host-originated requests:

```go
func (h *Host) SendControlRequest(
    ctx context.Context,
    req any,
) (json.RawMessage, error) {
    id := newRequestID()

    env := map[string]any{
        "type":       "control_request",
        "request_id": id,
        "request":    req,
    }

    b, err := json.Marshal(env)
    if err != nil {
        return nil, err
    }

    wait := make(chan ControlResponse, 1)

    h.mu.Lock()
    h.pendingControl[id] = &PendingControl{Resp: wait}
    h.mu.Unlock()

    defer func() {
        h.mu.Lock()
        delete(h.pendingControl, id)
        h.mu.Unlock()
    }()

    if err := h.transport.Send(ctx, append(b, '\n')); err != nil {
        return nil, err
    }

    select {
    case resp := <-wait:
        if resp.Subtype == "error" {
            return nil, errors.New(resp.Error)
        }
        return resp.Response, nil
    case <-ctx.Done():
        return nil, ctx.Err()
    }
}
```

## 20. Handling Incoming `mcp_message`

This is the critical handler.

Pseudocode:

```go
func (h *Host) handleIncomingMCP(
    ctx context.Context,
    outerRequestID string,
    serverName string,
    raw json.RawMessage,
) error {
    server := h.servers[serverName]
    if server == nil {
        return h.sendControlError(ctx, outerRequestID, "unknown SDK MCP server")
    }

    id, hasID := extractJSONRPCID(raw)
    if !hasID {
        if err := server.Inject(ctx, raw); err != nil {
            return h.sendControlError(ctx, outerRequestID, err.Error())
        }
        return h.sendControlSuccess(ctx, outerRequestID, map[string]any{
            "mcp_response": map[string]any{
                "jsonrpc": "2.0",
                "id":      0,
                "result":  map[string]any{},
            },
        })
    }

    key := serverName + ":" + canonicalJSONRPCID(id)
    ch := make(chan json.RawMessage, 1)

    h.mu.Lock()
    h.pendingMCP[key] = ch
    h.mu.Unlock()

    defer func() {
        h.mu.Lock()
        delete(h.pendingMCP, key)
        h.mu.Unlock()
    }()

    if err := server.Inject(ctx, raw); err != nil {
        return h.sendControlError(ctx, outerRequestID, err.Error())
    }

    select {
    case response := <-ch:
        return h.sendControlSuccess(ctx, outerRequestID, map[string]any{
            "mcp_response": json.RawMessage(response),
        })
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

## 21. Handling Server-Originated MCP Messages

When your in-process MCP server emits a message, the bridge callback should do:

```go
func (h *Host) OnServerMessage(
    ctx context.Context,
    serverName string,
    raw json.RawMessage,
) error {
    id, hasID := extractJSONRPCID(raw)
    if hasID {
        key := serverName + ":" + canonicalJSONRPCID(id)

        h.mu.Lock()
        ch := h.pendingMCP[key]
        h.mu.Unlock()

        if ch != nil {
            ch <- raw
            return nil
        }
    }

    _, err := h.SendControlRequest(ctx, map[string]any{
        "subtype":     "mcp_message",
        "server_name": serverName,
        "message":     json.RawMessage(raw),
    })
    return err
}
```

This is the full-duplex MCP bridge in one function.

## 22. Correlation Strategy

You need two different correlation tables.

### 22.1 Outer control correlation

Key:

- outer `request_id`

Used for:

- matching `control_response` to host-originated `control_request`

### 22.2 Inner MCP correlation

Key:

- `server_name + ":" + jsonrpc_id`

Used for:

- matching server-emitted JSON-RPC responses to Claude Code-initiated `mcp_message` waits

Do not mix these two layers together.

## 23. If Your Go MCP Library Has a Transport Interface

This is the best-case scenario.

Implement a small transport adapter per SDK-owned server:

- Claude Code -> host -> `transport.onmessage`
- server transport `send()` -> host callback -> either resolve pending waiter or forward back to Claude Code

That matches the cleanest architecture.

## 24. If Your Go MCP Library Does Not Expose a Transport Interface

Fallback option:

manually interpret a narrow set of MCP methods and implement them directly.

Typical minimum set:

- `initialize`
- `tools/list`
- `tools/call`
- `notifications/initialized`

This works, but it is more brittle:

- new MCP features may require new method handlers
- server-initiated requests get more awkward
- protocol drift is more likely

Only do this if your MCP library gives you no clean transport hook.

## 25. Minimal Viable Host

The smallest useful Go host should support:

1. process spawn in `stream-json` mode
2. explicit `initialize`
3. user message streaming
4. outer `control_request` / `control_response`
5. incoming `mcp_message`
6. reverse MCP traffic
7. `control_cancel_request`

If you need richer compatibility, add:

1. `can_use_tool`
2. `hook_callback`
3. `elicitation`
4. environment-ariable updates

## 26. Failure Modes to Avoid

### Closing stdin too early

Symptom:

- tool permission prompts break
- hooks break
- SDK MCP servers appear to hang

### Only implementing one-way MCP

Symptom:

- direct tool calls may work
- notifications or server-originated messages never arrive in Claude Code

### Using only one pending map

Symptom:

- hard-to-debug collisions between outer request IDs and inner JSON-RPC IDs

### Returning the wrong error shape

Wrong:

```json
{
  "error": {
    "message": "failed"
  }
}
```

Right:

```json
{
  "error": "failed"
}
```

### Forgetting to preserve JSON-RPC IDs

Symptom:

- Claude Code waits forever for an MCP response

## 27. Practical Checklist

Before calling the implementation done, verify:

1. Claude Code is launched with `stream-json` input and output
2. stdout is read continuously for the whole session
3. stdin stays open long enough for late callbacks
4. `initialize` is sent before the first prompt
5. SDK-owned MCP servers are registered by name
6. incoming `mcp_message` reaches the correct in-process server
7. server-emitted JSON-RPC responses resolve waiting MCP calls
8. server-emitted notifications are forwarded back to Claude Code
9. `control_cancel_request` cancels in-flight work
10. control errors use string payloads

## 28. Final Recommendation

If you are building this in Go:

- keep the outer control protocol and inner MCP protocol clearly separated
- use two correlation maps
- prefer a real transport bridge for SDK-managed MCP servers
- always send explicit `initialize`
- keep stdin open conservatively

That is the architecture most likely to behave correctly and remain maintainable as the protocol grows.

