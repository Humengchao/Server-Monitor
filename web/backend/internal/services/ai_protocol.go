package services

// Chat-completions tool calling is shared by the provider adapter and task
// worker. Model-provided arguments remain untrusted until PrepareAIAction.
type AIMessage struct {
	Role             string       `json:"role"`
	Content          string       `json:"content"`
	ReasoningContent string       `json:"reasoning_content,omitempty"`
	ToolCalls        []AIToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string       `json:"tool_call_id,omitempty"`
}

type AIToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function AIFunctionCall `json:"function"`
}

type AIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type AIToolDefinition struct {
	Type     string               `json:"type"`
	Function AIFunctionDefinition `json:"function"`
}

type AIFunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type AICompletion struct {
	Message          AIMessage
	PromptTokens     int64
	CompletionTokens int64
	FinishReason     string
}
