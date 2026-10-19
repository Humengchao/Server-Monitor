package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"server-monitor/internal/models"
)

const AIMCPToolLimit = 32
const AIMCPTimeout = 90 * time.Second
const aiMCPBodyLimit = 2 << 20
const aiMCPSchemaLimit = 16 << 10
const aiMCPProtocol = "2025-11-25"

var aiMCPName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

type AIMCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}
type AIMCPError struct{ Code string }

func (e *AIMCPError) Error() string {
	switch e.Code {
	case "plugin_disabled":
		return "MCP plugin is disabled"
	case "approval_required":
		return "MCP tool execution requires approval"
	case "authorization_revoked":
		return "MCP tool authorization changed before execution; no tool call was sent"
	case "readonly":
		return "MCP tool execution is unavailable in read-only mode"
	case "invalid_request":
		return "MCP tool arguments or plugin configuration are invalid"
	case "endpoint_restricted":
		return "MCP endpoint resolves to a prohibited address"
	case "dns_failed":
		return "MCP endpoint DNS lookup failed"
	case "authentication_failed":
		return "MCP plugin rejected its credentials"
	case "redirect_rejected":
		return "MCP redirects are not followed"
	case "session_expired":
		return "MCP session expired; the operation was not retried"
	case "rate_limited":
		return "MCP plugin rate limit reached; the operation was not retried"
	case "response_too_large":
		return "MCP response exceeds the size or tool-count limit"
	case "unsupported_protocol":
		return "MCP plugin does not support a compatible HTTP protocol or tools capability"
	case "unsupported_server_request":
		return "MCP server requested an unsupported client capability"
	case "remote_error":
		return "MCP server rejected the JSON-RPC request"
	case "invalid_response":
		return "MCP server returned an invalid response"
	case "credential_response":
		return "MCP response contained credentials and was withheld"
	case "timeout":
		return "MCP request timed out; no automatic retry was made"
	case "canceled":
		return "MCP request was canceled; no automatic retry was made"
	default:
		return "MCP connection or request failed"
	}
}
func SafeAIMCPError(err error) (string, string) {
	var safe *AIMCPError
	if errors.As(err, &safe) {
		return safe.Code, safe.Error()
	}
	code := "connection_failed"
	if errors.Is(err, context.Canceled) {
		code = "canceled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "timeout"
	}
	return code, (&AIMCPError{Code: code}).Error()
}
func aiMCPError(err error) error {
	if err == nil {
		return nil
	}
	var safe *AIMCPError
	if errors.As(err, &safe) {
		return safe
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var provider *AIProviderError
	if errors.As(err, &provider) {
		switch provider.Code {
		case "endpoint_restricted", "dns_failed":
			return &AIMCPError{Code: provider.Code}
		}
	}
	return &AIMCPError{Code: "connection_failed"}
}

// AIMCPClient uses the same HTTPS certificate validation, DNS pinning, address
// restrictions, redirect refusal and cancellation transport as model requests.
// No executable or stdio transport is supported.
type AIMCPClient struct{ transport *AIClient }

type aiMCPAuthorizationKey struct{}

// WithAIMCPAuthorization rechecks the worker's saved authority after the
// potentially slow handshake and DNS resolution, immediately before sending
// tools/call. A failed check never exposes its internal error to the plugin.
func WithAIMCPAuthorization(ctx context.Context, authorize func(context.Context) error) context.Context {
	return context.WithValue(ctx, aiMCPAuthorizationKey{}, authorize)
}

func NewAIMCPClient(transport *AIClient) *AIMCPClient { return &AIMCPClient{transport: transport} }
func (c *AIMCPClient) ValidateEndpoint(ctx context.Context, url string) error {
	return aiMCPError(c.transport.ValidateEndpoint(ctx, url))
}

type aiMCPSession struct {
	client      *AIMCPClient
	plugin      models.AIPlugin
	version, id string
	nextID      int
}

func (c *AIMCPClient) initialize(ctx context.Context, p models.AIPlugin) (*aiMCPSession, error) {
	if !p.Enabled {
		return nil, &AIMCPError{Code: "plugin_disabled"}
	}
	if models.NormalizeAIPlugin(&p) != nil {
		return nil, &AIMCPError{Code: "invalid_request"}
	}
	s := &aiMCPSession{client: c, plugin: p}
	ready := false
	defer func() {
		if !ready {
			s.close()
		}
	}()
	raw, _, err := s.post(ctx, "initialize", map[string]any{"protocolVersion": aiMCPProtocol, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "server-monitor", "version": "1"}}, false)
	if err != nil {
		return nil, err
	}
	var result struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return nil, &AIMCPError{Code: "invalid_response"}
	}
	switch result.ProtocolVersion {
	case "2025-03-26", "2025-06-18", aiMCPProtocol:
	default:
		return nil, &AIMCPError{Code: "unsupported_protocol"}
	}
	s.version = result.ProtocolVersion
	capability, ok := result.Capabilities["tools"]
	if !ok || len(capability) == 0 || capability[0] != '{' {
		return nil, &AIMCPError{Code: "unsupported_protocol"}
	}
	if _, _, err = s.post(ctx, "notifications/initialized", nil, true); err != nil {
		return nil, err
	}
	ready = true
	return s, nil
}

func (c *AIMCPClient) ListTools(ctx context.Context, p models.AIPlugin) ([]AIMCPTool, error) {
	ctx, cancel := context.WithTimeout(ctx, AIMCPTimeout)
	defer cancel()
	s, err := c.initialize(ctx, p)
	if err != nil {
		return nil, err
	}
	defer s.close()
	result := []AIMCPTool{}
	names := map[string]bool{}
	cursors := map[string]bool{}
	cursor := ""
	for page := 0; page < 8; page++ {
		params := map[string]string{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, _, err := s.post(ctx, "tools/list", params, false)
		if err != nil {
			return nil, err
		}
		if aiContainsEncodedToken(raw, p.BearerToken) {
			return nil, &AIMCPError{Code: "credential_response"}
		}
		var list struct {
			Tools      []AIMCPTool `json:"tools"`
			NextCursor string      `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &list) != nil || list.Tools == nil {
			return nil, &AIMCPError{Code: "invalid_response"}
		}
		if len(result)+len(list.Tools) > AIMCPToolLimit {
			return nil, &AIMCPError{Code: "response_too_large"}
		}
		for _, tool := range list.Tools {
			if !aiMCPName.MatchString(tool.Name) || names[tool.Name] || len(tool.Description) > 4096 || !utf8.ValidString(tool.Description) || strings.ContainsRune(tool.Description, 0) || len(tool.InputSchema) > aiMCPSchemaLimit {
				return nil, &AIMCPError{Code: "invalid_response"}
			}
			var schema map[string]any
			if validateAIMCPJSON(tool.InputSchema) != nil || json.Unmarshal(tool.InputSchema, &schema) != nil || schema["type"] != "object" {
				return nil, &AIMCPError{Code: "invalid_response"}
			}
			names[tool.Name] = true
			result = append(result, tool)
		}
		if list.NextCursor == "" {
			return result, nil
		}
		if len(list.NextCursor) > 2048 || cursors[list.NextCursor] || len(result) >= AIMCPToolLimit {
			return nil, &AIMCPError{Code: "response_too_large"}
		}
		cursors[list.NextCursor] = true
		cursor = list.NextCursor
	}
	return nil, &AIMCPError{Code: "response_too_large"}
}

// The task worker owns selection, snapshots and exact-action approval. This
// extra gate cannot be overridden by a plugin's readOnlyHint annotations.
func (c *AIMCPClient) CallTool(ctx context.Context, p models.AIPlugin, name string, args json.RawMessage, approved, readOnly bool) (result AIActionResult, err error) {
	start := time.Now()
	defer func() {
		result.DurationMS = time.Since(start).Milliseconds()
		if err != nil {
			_, result.Error = SafeAIMCPError(err)
		}
	}()
	if readOnly {
		return result, &AIMCPError{Code: "readonly"}
	}
	if !approved {
		return result, &AIMCPError{Code: "approval_required"}
	}
	if !aiMCPName.MatchString(name) || len(args) > AIArgumentsLimit || validateAIJSON(args) != nil || validateAIMCPJSON(args) != nil || aiContainsEncodedToken(args, p.BearerToken) {
		return result, &AIMCPError{Code: "invalid_request"}
	}
	ctx, cancel := context.WithTimeout(ctx, AIMCPTimeout)
	defer cancel()
	s, err := c.initialize(ctx, p)
	if err != nil {
		return result, err
	}
	defer s.close()
	raw, attempted, err := s.post(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, false)
	if err != nil {
		result.Unknown = attempted
		return result, err
	}
	var called struct {
		Content           []json.RawMessage `json:"content"`
		StructuredContent json.RawMessage   `json:"structuredContent"`
		IsError           json.RawMessage   `json:"isError"`
	}
	if json.Unmarshal(raw, &called) != nil || called.Content == nil || len(called.Content) > 128 ||
		len(called.IsError) != 0 && string(called.IsError) != "true" && string(called.IsError) != "false" ||
		len(called.StructuredContent) != 0 && called.StructuredContent[0] != '{' {
		result.Unknown = true
		return result, &AIMCPError{Code: "invalid_response"}
	}
	// Validate the fields we consume before recording a success. encoding/json
	// otherwise turns null or missing text into a successful empty tool result.
	var output strings.Builder
	for _, block := range called.Content {
		var content struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		if json.Unmarshal(block, &content) != nil || content.Type == "" || content.Type == "text" && content.Text == nil {
			result.Unknown = true
			return result, &AIMCPError{Code: "invalid_response"}
		}
		if content.Type == "text" {
			output.WriteString(*content.Text)
			output.WriteByte('\n')
		} else {
			output.WriteString("[non-text MCP content omitted]\n")
		}
	}
	if len(called.StructuredContent) > 0 {
		output.Write(called.StructuredContent)
	}
	if aiContainsEncodedToken(raw, p.BearerToken) || aiContainsSecrets(string(raw)) {
		result.Output = "[MCP output withheld: credential-like material]"
	} else {
		if aiContainsSecrets(output.String()) {
			result.Output = "[MCP output withheld: credential-like material]"
		} else {
			result.Output, result.Truncated = boundAIOutput(output.String(), false)
		}
	}
	if string(called.IsError) == "true" {
		result.Error = "MCP tool reported an execution error"
	}
	return result, nil
}

func (s *aiMCPSession) post(ctx context.Context, method string, params any, notification bool) (json.RawMessage, bool, error) {
	s.nextID++
	id := strconv.Itoa(s.nextID)
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if !notification {
		message["id"] = id
	}
	if params != nil {
		message["params"] = params
	}
	body, err := json.Marshal(message)
	if err != nil || len(body) > aiMCPBodyLimit {
		return nil, false, &AIMCPError{Code: "invalid_request"}
	}
	request, err := s.request(ctx, http.MethodPost, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	request.Header.Set("Content-Type", "application/json")
	if method == "tools/call" {
		if authorize, ok := ctx.Value(aiMCPAuthorizationKey{}).(func(context.Context) error); ok && authorize != nil {
			if authorize(ctx) != nil {
				return nil, false, &AIMCPError{Code: "authorization_revoked"}
			}
		}
	}
	response, err := s.client.transport.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, true, aiMCPError(err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, true, &AIMCPError{Code: "redirect_rejected"}
	}
	switch response.StatusCode {
	case 401, 403:
		return nil, true, &AIMCPError{Code: "authentication_failed"}
	case 404:
		return nil, true, &AIMCPError{Code: "session_expired"}
	case 429:
		return nil, true, &AIMCPError{Code: "rate_limited"}
	}
	if notification {
		if response.StatusCode != 202 {
			return nil, true, &AIMCPError{Code: "invalid_response"}
		}
		return nil, true, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, true, &AIMCPError{Code: "connection_failed"}
	}
	if method == "initialize" {
		sessionID := response.Header.Get("Mcp-Session-Id")
		if len(sessionID) > 4096 || strings.ContainsFunc(sessionID, func(r rune) bool { return r < 33 || r > 126 }) {
			return nil, true, &AIMCPError{Code: "invalid_response"}
		}
		s.id = sessionID
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		return nil, true, &AIMCPError{Code: "invalid_response"}
	}
	if contentType == "text/event-stream" {
		raw, err := readAIMCPSSE(response.Body, id)
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		return raw, true, err
	}
	if contentType != "application/json" {
		return nil, true, &AIMCPError{Code: "invalid_response"}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, aiMCPBodyLimit+1))
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, true, aiMCPError(err)
	}
	if len(raw) > aiMCPBodyLimit {
		return nil, true, &AIMCPError{Code: "response_too_large"}
	}
	result, done, err := parseAIMCPMessage(raw, id)
	if err == nil && !done {
		err = &AIMCPError{Code: "invalid_response"}
	}
	return result, true, err
}

func (s *aiMCPSession) request(ctx context.Context, method string, body io.Reader) (*http.Request, error) {
	pin, err := s.client.transport.resolveEndpoint(ctx, s.plugin.URL)
	if err != nil {
		return nil, aiMCPError(err)
	}
	ctx = context.WithValue(ctx, aiPinnedEndpointKey{}, pin)
	request, err := http.NewRequestWithContext(ctx, method, s.plugin.URL, body)
	if err != nil {
		return nil, &AIMCPError{Code: "invalid_request"}
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	if s.plugin.BearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+s.plugin.BearerToken)
	}
	if s.id != "" {
		request.Header.Set("Mcp-Session-Id", s.id)
	}
	if s.version != "" {
		request.Header.Set("Mcp-Protocol-Version", s.version)
	}
	return request, nil
}

// A session can outlive each HTTP connection. End it once, even when the
// operation was canceled, without retrying a tool or replacing its result.
// Servers may refuse DELETE with 405; all cleanup errors are best-effort.
func (s *aiMCPSession) close() {
	if s.id == "" {
		return
	}
	id := s.id
	s.id = ""
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := s.request(ctx, http.MethodDelete, nil)
	if err != nil {
		return
	}
	request.Header.Set("Mcp-Session-Id", id)
	if s.version == "" {
		request.Header.Set("Mcp-Protocol-Version", aiMCPProtocol)
	}
	response, err := s.client.transport.client.Do(request)
	if err != nil {
		return
	}
	_ = response.Body.Close()
}

func parseAIMCPMessage(raw []byte, id string) (json.RawMessage, bool, error) {
	bad := &AIMCPError{Code: "invalid_response"}
	if validateAIMCPJSON(raw) != nil {
		return nil, false, bad
	}
	var message struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &message) != nil || message.JSONRPC != "2.0" {
		return nil, false, bad
	}
	if message.Method != "" {
		if len(message.ID) > 0 {
			return nil, false, &AIMCPError{Code: "unsupported_server_request"}
		}
		if !strings.HasPrefix(message.Method, "notifications/") || len(message.Result) > 0 || len(message.Error) > 0 {
			return nil, false, bad
		}
		return nil, false, nil
	}
	var returnedID string
	if json.Unmarshal(message.ID, &returnedID) != nil || returnedID != id {
		return nil, false, bad
	}
	if len(message.Error) > 0 {
		if len(message.Result) > 0 {
			return nil, false, bad
		}
		return nil, true, &AIMCPError{Code: "remote_error"}
	}
	if len(message.Result) == 0 || bytes.Equal(message.Result, []byte("null")) || message.Result[0] != '{' {
		return nil, false, bad
	}
	return message.Result, true, nil
}
func readAIMCPSSE(reader io.Reader, id string) (json.RawMessage, error) {
	limited := &io.LimitedReader{R: reader, N: aiMCPBodyLimit + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), aiMCPBodyLimit+1)
	var data strings.Builder
	events := 0
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			events++
			if events > 128 {
				return nil, &AIMCPError{Code: "response_too_large"}
			}
			if data.Len() == 0 {
				continue
			}
			raw := strings.TrimSuffix(data.String(), "\n")
			data.Reset()
			if raw == "" {
				continue
			}
			result, done, err := parseAIMCPMessage([]byte(raw), id)
			if err != nil {
				return nil, err
			}
			if done {
				return result, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			data.WriteString(value)
			data.WriteByte('\n')
		}
		if limited.N <= 0 {
			return nil, &AIMCPError{Code: "response_too_large"}
		}
	}
	if limited.N <= 0 {
		return nil, &AIMCPError{Code: "response_too_large"}
	}
	return nil, &AIMCPError{Code: "invalid_response"}
}

func validateAIMCPJSON(raw []byte) error {
	// Go's struct decoder folds field names, so case aliases are duplicates
	// too (isError/IsError). NUL strings cannot be persisted to PostgreSQL.
	if !validAIProtocolObject(raw, aiMCPBodyLimit) {
		return errors.New("invalid JSON object")
	}
	return nil
}
