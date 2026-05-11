package sprawlmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// askQuestionToolCall builds and dispatches a tools/call request for
// ask_user_question with the given caller identity injected into ctx and the
// given arguments object. Returns the parsed JSON-RPC response.
func askQuestionToolCall(t *testing.T, srv *Server, caller string, args any) map[string]any {
	t.Helper()
	ctx := context.Background()
	if caller != "" {
		ctx = withTestCallerIdentity(ctx, caller)
	}
	msg := makeJSONRPCRequest(42, "tools/call", map[string]any{
		"name":      "ask_user_question",
		"arguments": args,
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}
	return parseJSONRPCResponse(t, resp)
}

// toolResult extracts the tool content from a JSON-RPC response. Returns
// (text, isError). Fails if the response is a JSON-RPC error (which would
// indicate an unknown-tool/protocol issue rather than a tool-level error).
func toolResult(t *testing.T, parsed map[string]any) (string, bool) {
	t.Helper()
	if errObj, ok := parsed["error"]; ok {
		t.Fatalf("unexpected JSON-RPC error: %v", errObj)
	}
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result in response: %v", parsed)
	}
	isErr, _ := result["isError"].(bool)
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing content in result: %v", result)
	}
	first := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text, isErr
}

// validQuestion is the minimal valid arguments object for the tool.
func validQuestionArgs() map[string]any {
	return map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"header":   "Scope",
				"question": "Which path?",
				"options": []map[string]any{
					{"label": "A"},
					{"label": "B", "description": "the second"},
				},
			},
		},
	}
}

// ============================================================================
// Eligibility gate
// ============================================================================

func TestAskUserQuestion_Eligibility_RootWeaveAllowed(t *testing.T) {
	mock := &mockSupervisor{
		askQuestionResult: supervisor.QuestionResponse{
			RequestID: "ignored-by-tool",
			Outcome:   supervisor.OutcomeAnswered,
		},
	}
	srv := New(mock)

	parsed := askQuestionToolCall(t, srv, "", validQuestionArgs())
	_, isErr := toolResult(t, parsed)
	if isErr {
		t.Fatalf("expected root (weave) caller to be allowed, got isError=true")
	}
	if mock.askQuestionCalls != 1 {
		t.Errorf("AskUserQuestion calls = %d, want 1", mock.askQuestionCalls)
	}
}

// QUM-527: when weave is registered as a root runtime, its caller identity is
// "weave" (not ""), so the empty-caller short-circuit does not fire. The
// eligibility gate must therefore accept Type=="root" as equivalent to
// Type=="manager" for this caller.
func TestAskUserQuestion_Eligibility_RootWeaveAllowed_NonEmptyCaller(t *testing.T) {
	mock := &mockSupervisor{
		statusResult: []supervisor.AgentInfo{
			{Name: "weave", Type: "root"},
		},
		askQuestionResult: supervisor.QuestionResponse{
			RequestID: "rid",
			Outcome:   supervisor.OutcomeAnswered,
		},
	}
	srv := New(mock)

	parsed := askQuestionToolCall(t, srv, "weave", validQuestionArgs())
	text, isErr := toolResult(t, parsed)
	if isErr {
		t.Fatalf("expected root-typed weave caller to be allowed, got isError=true: %s", text)
	}
	if mock.askQuestionCalls != 1 {
		t.Errorf("AskUserQuestion calls = %d, want 1", mock.askQuestionCalls)
	}
}

// QUM-527: documents that the fix relies on the agent record carrying
// Type=="root" — not on the magic name "weave". An agent record named "weave"
// with an empty Type must still be rejected by the eligibility gate.
func TestAskUserQuestion_Eligibility_WeaveWithEmptyTypeRejected(t *testing.T) {
	mock := &mockSupervisor{
		statusResult: []supervisor.AgentInfo{
			{Name: "weave", Type: ""},
		},
	}
	srv := New(mock)

	parsed := askQuestionToolCall(t, srv, "weave", validQuestionArgs())
	text, isErr := toolResult(t, parsed)
	if !isErr {
		t.Fatalf("expected weave-with-empty-Type to be rejected, got success: %s", text)
	}
	if !strings.Contains(text, "ask_user_question is restricted") {
		t.Errorf("error text missing canonical message; got: %s", text)
	}
	if mock.askQuestionCalls != 0 {
		t.Errorf("AskUserQuestion calls = %d, want 0", mock.askQuestionCalls)
	}
}

func TestAskUserQuestion_Eligibility_ManagerAllowed(t *testing.T) {
	mock := &mockSupervisor{
		statusResult: []supervisor.AgentInfo{
			{Name: "boss", Type: "manager", Family: "engineering"},
		},
		askQuestionResult: supervisor.QuestionResponse{
			RequestID: "rid",
			Outcome:   supervisor.OutcomeAnswered,
		},
	}
	srv := New(mock)

	parsed := askQuestionToolCall(t, srv, "boss", validQuestionArgs())
	_, isErr := toolResult(t, parsed)
	if isErr {
		t.Fatalf("expected manager caller to be allowed, got isError=true")
	}
	if mock.askQuestionCalls != 1 {
		t.Errorf("AskUserQuestion calls = %d, want 1", mock.askQuestionCalls)
	}
}

func TestAskUserQuestion_Eligibility_EngineerRejected(t *testing.T) {
	mock := &mockSupervisor{
		statusResult: []supervisor.AgentInfo{
			{Name: "ratz", Type: "engineer", Family: "engineering"},
		},
	}
	srv := New(mock)

	parsed := askQuestionToolCall(t, srv, "ratz", validQuestionArgs())
	text, isErr := toolResult(t, parsed)
	if !isErr {
		t.Fatalf("expected engineer caller to be rejected (isError=true), got success: %s", text)
	}
	if !strings.Contains(text, "ask_user_question is restricted to weave and managers") {
		t.Errorf("error text missing canonical message; got: %s", text)
	}
	if !strings.Contains(text, "escalate to your parent") {
		t.Errorf("error text should tell engineer to escalate; got: %s", text)
	}
	// Eligibility must reject *before* dispatching to the supervisor.
	if mock.askQuestionCalls != 0 {
		t.Errorf("AskUserQuestion calls = %d, want 0 (eligibility gate must block)", mock.askQuestionCalls)
	}

	// The error text must be JSON-parseable with an "error" key so agents can
	// programmatically detect this case.
	var parsedErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &parsedErr); err != nil {
		t.Fatalf("error text is not JSON: %v\ntext: %s", err, text)
	}
	if parsedErr.Error == "" {
		t.Errorf("error JSON missing 'error' key; got: %s", text)
	}
}

func TestAskUserQuestion_Eligibility_ResearcherRejected(t *testing.T) {
	mock := &mockSupervisor{
		statusResult: []supervisor.AgentInfo{
			{Name: "ghost", Type: "researcher", Family: "engineering"},
		},
	}
	srv := New(mock)

	parsed := askQuestionToolCall(t, srv, "ghost", validQuestionArgs())
	text, isErr := toolResult(t, parsed)
	if !isErr {
		t.Fatalf("expected researcher caller to be rejected, got success: %s", text)
	}
	if !strings.Contains(text, "ask_user_question is restricted") {
		t.Errorf("error text missing canonical message; got: %s", text)
	}
	if mock.askQuestionCalls != 0 {
		t.Errorf("AskUserQuestion calls = %d, want 0", mock.askQuestionCalls)
	}
}

// ============================================================================
// Argument parsing
// ============================================================================

func TestAskUserQuestion_EmptyQuestionsArrayRejected(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	parsed := askQuestionToolCall(t, srv, "", map[string]any{
		"questions": []map[string]any{},
	})
	_, isErr := toolResult(t, parsed)
	if !isErr {
		t.Fatal("expected isError=true for empty questions[]")
	}
	if mock.askQuestionCalls != 0 {
		t.Errorf("supervisor must not be called when questions[] is empty")
	}
}

func TestAskUserQuestion_MissingQuestionFieldRejected(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	parsed := askQuestionToolCall(t, srv, "", map[string]any{
		"questions": []map[string]any{
			{
				"header": "scope",
				// no "question" field
				"options": []map[string]any{{"label": "A"}},
			},
		},
	})
	_, isErr := toolResult(t, parsed)
	if !isErr {
		t.Fatal("expected isError=true when question field is missing")
	}
	if mock.askQuestionCalls != 0 {
		t.Errorf("supervisor must not be called when question is missing")
	}
}

func TestAskUserQuestion_MissingOptionsRejected(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	parsed := askQuestionToolCall(t, srv, "", map[string]any{
		"questions": []map[string]any{
			{
				"question": "Pick a path",
				// no "options" field
			},
		},
	})
	_, isErr := toolResult(t, parsed)
	if !isErr {
		t.Fatal("expected isError=true when options is missing")
	}
	if mock.askQuestionCalls != 0 {
		t.Errorf("supervisor must not be called when options is missing")
	}
}

func TestAskUserQuestion_ValidArgsCallsSupervisorOnce(t *testing.T) {
	mock := &mockSupervisor{
		askQuestionResult: supervisor.QuestionResponse{
			Outcome: supervisor.OutcomeAnswered,
		},
	}
	srv := New(mock)

	parsed := askQuestionToolCall(t, srv, "", validQuestionArgs())
	_, isErr := toolResult(t, parsed)
	if isErr {
		t.Fatal("unexpected isError=true on valid input")
	}
	if mock.askQuestionCalls != 1 {
		t.Errorf("AskUserQuestion calls = %d, want exactly 1", mock.askQuestionCalls)
	}

	req := mock.askQuestionLast
	if req.RequestID == "" {
		t.Errorf("RequestID was empty; the dispatcher must generate a stable id")
	}
	if len(req.Questions) != 1 {
		t.Fatalf("Questions length = %d, want 1", len(req.Questions))
	}
	if req.Questions[0].Prompt != "Which path?" {
		t.Errorf("Question.Prompt = %q, want %q", req.Questions[0].Prompt, "Which path?")
	}
	if req.Questions[0].Header != "Scope" {
		t.Errorf("Question.Header = %q, want %q", req.Questions[0].Header, "Scope")
	}
	if len(req.Questions[0].Options) != 2 {
		t.Errorf("Question.Options length = %d, want 2", len(req.Questions[0].Options))
	}
}

// ============================================================================
// Snake-case-only — camelCase multiSelect must NOT bind to multi_select.
// ============================================================================

func TestAskUserQuestion_CamelCaseMultiSelectIgnored(t *testing.T) {
	mock := &mockSupervisor{
		askQuestionResult: supervisor.QuestionResponse{Outcome: supervisor.OutcomeAnswered},
	}
	srv := New(mock)

	args := map[string]any{
		"questions": []map[string]any{
			{
				"question":    "Pick many",
				"multiSelect": true, // camelCase — should NOT bind to multi_select
				"options":     []map[string]any{{"label": "A"}, {"label": "B"}},
			},
		},
	}
	parsed := askQuestionToolCall(t, srv, "", args)
	_, isErr := toolResult(t, parsed)
	if isErr {
		t.Fatalf("expected success (no strict-unknown-field rejection), got isError")
	}
	if mock.askQuestionCalls != 1 {
		t.Fatalf("AskUserQuestion calls = %d, want 1", mock.askQuestionCalls)
	}

	if mock.askQuestionLast.Questions[0].MultiSelect {
		t.Errorf("camelCase multiSelect must not bind to multi_select; got MultiSelect=true")
	}
}

func TestAskUserQuestion_SnakeCaseMultiSelectBinds(t *testing.T) {
	mock := &mockSupervisor{
		askQuestionResult: supervisor.QuestionResponse{Outcome: supervisor.OutcomeAnswered},
	}
	srv := New(mock)

	args := map[string]any{
		"questions": []map[string]any{
			{
				"question":     "Pick many",
				"multi_select": true,
				"options":      []map[string]any{{"label": "A"}, {"label": "B"}},
			},
		},
	}
	parsed := askQuestionToolCall(t, srv, "", args)
	_, isErr := toolResult(t, parsed)
	if isErr {
		t.Fatalf("unexpected isError on valid snake_case args")
	}
	if !mock.askQuestionLast.Questions[0].MultiSelect {
		t.Errorf("snake_case multi_select must bind to MultiSelect")
	}
}

// ============================================================================
// Response marshaling
// ============================================================================

func TestAskUserQuestion_ResponseMarshaledAsJSON(t *testing.T) {
	mock := &mockSupervisor{
		askQuestionResult: supervisor.QuestionResponse{
			RequestID: "rid-xyz",
			Outcome:   supervisor.OutcomeAnswered,
			Answers: []supervisor.QuestionAnswer{
				{QuestionID: "q1", Selected: []string{"A"}},
			},
			Note: "user picked option A",
		},
	}
	srv := New(mock)

	parsed := askQuestionToolCall(t, srv, "", validQuestionArgs())
	text, isErr := toolResult(t, parsed)
	if isErr {
		t.Fatalf("unexpected isError=true: %s", text)
	}

	var got supervisor.QuestionResponse
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("tool result text is not JSON QuestionResponse: %v\ntext: %s", err, text)
	}
	if got.RequestID != "rid-xyz" {
		t.Errorf("RequestID = %q, want rid-xyz", got.RequestID)
	}
	if got.Outcome != supervisor.OutcomeAnswered {
		t.Errorf("Outcome = %q, want answered", got.Outcome)
	}
	if len(got.Answers) != 1 || got.Answers[0].QuestionID != "q1" {
		t.Errorf("Answers not round-tripped: %#v", got.Answers)
	}
	if got.Note != "user picked option A" {
		t.Errorf("Note = %q, want %q", got.Note, "user picked option A")
	}
}

// ============================================================================
// Tool registration
// ============================================================================

func TestAskUserQuestion_RegisteredInToolsList(t *testing.T) {
	srv := New(&mockSupervisor{})
	ctx := context.Background()

	msg := makeJSONRPCRequest(99, "tools/list", nil)
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage error: %v", err)
	}
	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	tools := result["tools"].([]any)

	var found map[string]any
	for _, raw := range tools {
		tool := raw.(map[string]any)
		if tool["name"] == "ask_user_question" {
			found = tool
			break
		}
	}
	if found == nil {
		t.Fatal("ask_user_question not present in tools/list")
	}

	desc, _ := found["description"].(string)
	wantSubstr := "Use this when you feel a question must be directly escalated to the human user. Engineers and researchers: escalate to your parent manager instead."
	if !strings.Contains(desc, wantSubstr) {
		t.Errorf("description missing mandated text.\nwant substring: %q\ngot: %q", wantSubstr, desc)
	}

	// Schema sanity: snake_case multi_select; required {question, options}.
	schema, ok := found["inputSchema"].(map[string]any)
	if !ok {
		t.Fatal("missing inputSchema")
	}
	props := schema["properties"].(map[string]any)
	qProp, ok := props["questions"].(map[string]any)
	if !ok {
		t.Fatal("schema missing questions property")
	}
	items := qProp["items"].(map[string]any)
	itemProps := items["properties"].(map[string]any)
	if _, ok := itemProps["multi_select"]; !ok {
		t.Errorf("schema items missing snake_case multi_select")
	}
	required, _ := items["required"].([]string)
	if required == nil {
		// allow []any too
		if rawReq, ok := items["required"].([]any); ok {
			required = make([]string, len(rawReq))
			for i, v := range rawReq {
				required[i], _ = v.(string)
			}
		}
	}
	hasQ, hasOpts := false, false
	for _, r := range required {
		if r == "question" {
			hasQ = true
		}
		if r == "options" {
			hasOpts = true
		}
	}
	if !hasQ || !hasOpts {
		t.Errorf("required fields = %v, want [question options]", required)
	}
}

func TestMCPToolNames_IncludesAskUserQuestion(t *testing.T) {
	names := MCPToolNames()
	want := "mcp__sprawl__ask_user_question"
	for _, n := range names {
		if n == want {
			return
		}
	}
	t.Errorf("MCPToolNames() missing %q; got: %v", want, names)
}
