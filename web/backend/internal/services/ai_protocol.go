package services

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// encoding/json normally accepts duplicate keys, with the later value winning.
// Executable completions must not reinterpret a truncated finish_reason or
// tool name that way. Match keys case-insensitively as Go's struct decoder does.
// Reject NUL as well: PostgreSQL JSONB cannot persist it in a conversation.
func validAIProtocolObject(raw []byte, limit int) bool {
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) {
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var readValue func(int) bool
	readValue = func(depth int) bool {
		if depth > 32 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if value, ok := token.(string); ok {
			return !strings.ContainsRune(value, 0)
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || strings.ContainsRune(key, 0) {
					return false
				}
				key = strings.Map(func(r rune) rune {
					// EqualFold includes aliases such as long-s and Kelvin-sign,
					// unlike ToLower alone. Pick the minimum rune in that cycle.
					for {
						next := unicode.SimpleFold(r)
						if next <= r {
							return next
						}
						r = next
					}
				}, key)
				if seen[key] {
					return false
				}
				seen[key] = true
				if !readValue(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for decoder.More() {
				if !readValue(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !readValue(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

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
	ID           string              `json:"id"`
	Type         string              `json:"type"`
	Function     AIFunctionCall      `json:"function"`
	ExtraContent *AIToolExtraContent `json:"extra_content,omitempty"`
}

// Gemini's OpenAI-compatible endpoint requires the original thought signature
// on continued tool calls. Retain only this documented opaque field, not
// arbitrary provider extensions. It stays in the private model conversation.
type AIToolExtraContent struct {
	Google *AIGoogleToolContent `json:"google,omitempty"`
}

type AIGoogleToolContent struct {
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

func aiToolSignature(call AIToolCall) string {
	if call.ExtraContent == nil || call.ExtraContent.Google == nil {
		return ""
	}
	return call.ExtraContent.Google.ThoughtSignature
}

func validAIToolSignature(call AIToolCall) bool {
	signature := aiToolSignature(call)
	return len(signature) <= 65536 && !strings.ContainsFunc(signature, func(r rune) bool { return r < 33 || r > 126 })
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
