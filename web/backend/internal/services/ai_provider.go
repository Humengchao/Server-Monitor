package services

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode"

	"server-monitor/internal/models"
)

const AIProviderTimeout = 90 * time.Second
const AIProviderBodyLimit = 2 << 20
const aiRequestBodyLimit = 2 << 20
const aiMaxToolCalls = 32

var aiFunctionName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// AIProviderError deliberately excludes upstream bodies, URLs, keys, and
// network exception text. Its stable Code is safe for API responses/task logs.
type AIProviderError struct{ Code string }

func (e *AIProviderError) Error() string {
	switch e.Code {
	case "provider_disabled":
		return "AI provider is disabled"
	case "invalid_configuration":
		return "AI provider configuration is invalid"
	case "endpoint_restricted":
		return "AI endpoint resolves to a prohibited address"
	case "dns_failed":
		return "AI endpoint DNS lookup failed"
	case "authentication_failed":
		return "AI provider rejected the API key"
	case "rate_limited":
		return "AI provider rate limit reached; no automatic retry was made"
	case "upstream_unavailable":
		return "AI provider is temporarily unavailable"
	case "request_rejected":
		return "AI provider rejected the request"
	case "redirect_rejected":
		return "AI provider redirects are not followed"
	case "connection_failed":
		return "AI connection or TLS certificate verification failed"
	case "response_too_large":
		return "AI response exceeded the 2 MiB limit"
	case "invalid_response":
		return "AI provider returned an invalid chat completion"
	case "invalid_models_response":
		return "AI provider returned an invalid model catalog; enter a model ID manually"
	case "models_not_supported":
		return "AI provider does not support model discovery at this endpoint; enter a model ID manually"
	case "incomplete_response":
		return "AI generation did not finish; no tool actions were accepted"
	case "invalid_request":
		return "AI messages or tool definitions are invalid or exceed the limit"
	case "timeout":
		return "AI provider request timed out; no automatic retry was made"
	case "canceled":
		return "AI provider request was canceled"
	default:
		return "AI provider request failed"
	}
}
func SafeAIProviderError(err error) (string, string) {
	var safe *AIProviderError
	if errors.As(err, &safe) {
		return safe.Code, safe.Error()
	}
	if errors.Is(err, context.Canceled) {
		return "canceled", (&AIProviderError{Code: "canceled"}).Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", (&AIProviderError{Code: "timeout"}).Error()
	}
	return "provider_failed", "AI provider request failed"
}

type aiPinnedEndpoint struct {
	Host string
	Port string
	IPs  []net.IP
	// net/http deliberately detaches DialContext from Request cancellation.
	// This one-shot client retains the caller context to stop every stage.
	requestContext context.Context
}
type aiPinnedEndpointKey struct{}
type AIClient struct {
	client       *http.Client
	allowPrivate bool
	lookupIP     func(context.Context, string) ([]net.IP, error)
	dialContext  func(context.Context, string, string) (net.Conn, error)
}

func NewAIClient(allowPrivate bool) *AIClient {
	c := &AIClient{allowPrivate: allowPrivate,
		lookupIP: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		},
		dialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: -1}).DialContext,
	}
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: AIProviderTimeout, MaxResponseHeaderBytes: 64 << 10,
		DialContext: c.dialPinned,
	}
	c.client = &http.Client{Transport: transport, Timeout: AIProviderTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c
}

func aiCompletionEndpoint(baseURL string) (string, error) {
	parsed, err := models.ParseAIBaseURL(baseURL)
	if err != nil {
		return "", &AIProviderError{Code: "invalid_configuration"}
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/chat/completions") {
		path += "/chat/completions"
	}
	parsed.Path = path
	return parsed.String(), nil
}

var aiRestrictedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("2001:10::/28"), netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
}

func aiPublicIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range aiRestrictedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	// Public IPv6 endpoints currently use the global unicast allocation.
	if address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}
	return true
}
func (c *AIClient) resolveEndpoint(ctx context.Context, endpoint string) (aiPinnedEndpoint, error) {
	if err := ctx.Err(); err != nil {
		return aiPinnedEndpoint{}, err
	}
	parsed, err := models.ParseAIBaseURL(endpoint)
	if err != nil {
		return aiPinnedEndpoint{}, &AIProviderError{Code: "invalid_configuration"}
	}
	host := parsed.Hostname()
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = c.lookupIP(ctx, host)
		if err != nil {
			if ctx.Err() != nil {
				return aiPinnedEndpoint{}, ctx.Err()
			}
			return aiPinnedEndpoint{}, &AIProviderError{Code: "dns_failed"}
		}
	}
	if len(ips) == 0 || len(ips) > 32 {
		return aiPinnedEndpoint{}, &AIProviderError{Code: "dns_failed"}
	}
	copied := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if ip == nil || !c.allowPrivate && !aiPublicIP(ip) {
			return aiPinnedEndpoint{}, &AIProviderError{Code: "endpoint_restricted"}
		}
		copied = append(copied, append(net.IP(nil), ip...))
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	if err := ctx.Err(); err != nil {
		return aiPinnedEndpoint{}, err
	}
	return aiPinnedEndpoint{Host: host, Port: port, IPs: copied, requestContext: ctx}, nil
}
func (c *AIClient) ValidateEndpoint(ctx context.Context, baseURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := c.resolveEndpoint(ctx, baseURL)
	return err
}
func (c *AIClient) dialPinned(ctx context.Context, network, address string) (net.Conn, error) {
	pin, ok := ctx.Value(aiPinnedEndpointKey{}).(aiPinnedEndpoint)
	host, port, err := net.SplitHostPort(address)
	if !ok || err != nil || !strings.EqualFold(host, pin.Host) || port != pin.Port || len(pin.IPs) == 0 {
		return nil, &AIProviderError{Code: "endpoint_restricted"}
	}
	if pin.requestContext == nil {
		return nil, &AIProviderError{Code: "endpoint_restricted"}
	}
	if err := pin.requestContext.Err(); err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(pin.requestContext, cancel)
	defer func() { stopCancellation(); cancel() }()
	for _, ip := range pin.IPs {
		conn, err := c.dialContext(dialCtx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			// Keep cancellation attached after dialing too: net/http's TLS
			// handshake otherwise uses the same detached transport context.
			stop := context.AfterFunc(pin.requestContext, func() { _ = conn.Close() })
			return &aiRequestConn{Conn: conn, stopCancellation: stop}, nil
		}
		if pin.requestContext.Err() != nil {
			return nil, pin.requestContext.Err()
		}
		if dialCtx.Err() != nil {
			return nil, dialCtx.Err()
		}
	}
	return nil, &AIProviderError{Code: "connection_failed"}
}

type aiRequestConn struct {
	net.Conn
	stopCancellation func() bool
}

func (c *aiRequestConn) Close() error {
	c.stopCancellation()
	return c.Conn.Close()
}

func validateAIInput(messages []AIMessage, tools []AIToolDefinition) (map[string]bool, error) {
	bad := &AIProviderError{Code: "invalid_request"}
	if len(messages) == 0 || len(messages) > 512 || len(tools) > 128 {
		return nil, bad
	}
	names := map[string]bool{}
	for _, tool := range tools {
		if tool.Type != "function" || !aiFunctionName.MatchString(tool.Function.Name) || names[tool.Function.Name] || len(tool.Function.Description) > 16384 {
			return nil, bad
		}
		if tool.Function.Parameters != nil {
			if kind, ok := tool.Function.Parameters["type"]; ok && kind != "object" {
				return nil, bad
			}
		}
		names[tool.Function.Name] = true
	}
	inputBytes := 0
	for _, message := range messages {
		inputBytes += len(message.Content) + len(message.ReasoningContent) + len(message.ToolCallID)
		for _, call := range message.ToolCalls {
			inputBytes += len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
			inputBytes += len(aiToolSignature(call))
			if !validAIToolSignature(call) {
				return nil, bad
			}
		}
		if inputBytes > aiRequestBodyLimit {
			return nil, bad
		}
		switch message.Role {
		case "system", "user", "assistant", "tool":
		default:
			return nil, bad
		}
		if message.Role != "assistant" && (len(message.ToolCalls) != 0 || message.ReasoningContent != "") {
			return nil, bad
		}
		if message.Role == "tool" && message.ToolCallID == "" {
			return nil, bad
		}
		if message.Role != "tool" && message.ToolCallID != "" {
			return nil, bad
		}
		if len(message.ToolCalls) > aiMaxToolCalls {
			return nil, bad
		}
	}
	return names, nil
}

func (c *AIClient) Complete(ctx context.Context, provider models.AIProvider, messages []AIMessage, tools []AIToolDefinition) (AICompletion, error) {
	return c.complete(ctx, provider, messages, tools, 4096)
}
func (c *AIClient) complete(ctx context.Context, provider models.AIProvider, messages []AIMessage, tools []AIToolDefinition, maxTokens int) (AICompletion, error) {
	var empty AICompletion
	if !provider.Enabled {
		return empty, &AIProviderError{Code: "provider_disabled"}
	}
	if err := models.NormalizeAIProvider(&provider); err != nil || provider.APIKey == "" {
		return empty, &AIProviderError{Code: "invalid_configuration"}
	}
	allowed, err := validateAIInput(messages, tools)
	if err != nil {
		return empty, err
	}
	body := map[string]any{"model": provider.Model, "messages": messages, "stream": false, "max_tokens": maxTokens}
	if provider.Kind == "openai" {
		delete(body, "max_tokens")
		body["max_completion_tokens"] = maxTokens
	}
	if len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	if provider.Kind == "deepseek" {
		body["thinking"] = map[string]string{"type": "disabled"}
	}
	data, err := json.Marshal(body)
	if err != nil || len(data) > aiRequestBodyLimit {
		return empty, &AIProviderError{Code: "invalid_request"}
	}
	endpoint, err := aiCompletionEndpoint(provider.BaseURL)
	if err != nil {
		return empty, err
	}
	raw, err := c.requestJSON(ctx, provider, http.MethodPost, endpoint, data)
	if err != nil {
		return empty, err
	}
	return parseAICompletion(raw, allowed)
}

// Both model discovery and chat completions use the same one-shot pinned TLS
// transport. No upstream response body or network exception leaves this layer.
func (c *AIClient) requestJSON(ctx context.Context, provider models.AIProvider, method, endpoint string, data []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, AIProviderTimeout)
	defer cancel()
	pin, err := c.resolveEndpoint(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, aiPinnedEndpointKey{}, pin)
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, &AIProviderError{Code: "invalid_configuration"}
	}
	request.Header.Set("Authorization", "Bearer "+provider.APIKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "server-monitor-ai/1")
	response, err := c.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &AIProviderError{Code: "connection_failed"}
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode >= 300 && response.StatusCode < 400:
		return nil, &AIProviderError{Code: "redirect_rejected"}
	case response.StatusCode == 401 || response.StatusCode == 403:
		return nil, &AIProviderError{Code: "authentication_failed"}
	case response.StatusCode == 429:
		return nil, &AIProviderError{Code: "rate_limited"}
	case method == http.MethodGet && (response.StatusCode == 404 || response.StatusCode == 405 || response.StatusCode == 501):
		return nil, &AIProviderError{Code: "models_not_supported"}
	case response.StatusCode >= 500:
		return nil, &AIProviderError{Code: "upstream_unavailable"}
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return nil, &AIProviderError{Code: "request_rejected"}
	}
	if response.ContentLength > AIProviderBodyLimit {
		return nil, &AIProviderError{Code: "response_too_large"}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, AIProviderBodyLimit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &AIProviderError{Code: "invalid_response"}
	}
	if len(raw) > AIProviderBodyLimit {
		return nil, &AIProviderError{Code: "response_too_large"}
	}
	return raw, nil
}

func parseAICompletion(raw []byte, allowed map[string]bool) (AICompletion, error) {
	var empty AICompletion
	bad := &AIProviderError{Code: "invalid_response"}
	var response struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Index        int    `json:"index"`
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role             string       `json:"role"`
				Content          *string      `json:"content"`
				ReasoningContent *string      `json:"reasoning_content"`
				ToolCalls        []AIToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if !validAIProtocolObject(raw, AIProviderBodyLimit) || json.Unmarshal(raw, &response) != nil || len(response.Choices) != 1 || (len(response.Error) > 0 && string(response.Error) != "null") {
		return empty, bad
	}
	choice := response.Choices[0]
	if choice.Index != 0 || choice.Message.Role != "assistant" || response.Usage.PromptTokens < 0 || response.Usage.CompletionTokens < 0 {
		return empty, bad
	}
	message := AIMessage{Role: "assistant", ToolCalls: choice.Message.ToolCalls}
	if choice.Message.Content != nil {
		message.Content = *choice.Message.Content
	}
	if choice.Message.ReasoningContent != nil {
		message.ReasoningContent = *choice.Message.ReasoningContent
	}
	switch choice.FinishReason {
	case "stop":
		if len(message.ToolCalls) > 0 || strings.TrimSpace(message.Content) == "" {
			return empty, bad
		}
	case "tool_calls":
		if len(message.ToolCalls) == 0 || len(message.ToolCalls) > aiMaxToolCalls {
			return empty, bad
		}
		ids := map[string]bool{}
		for _, call := range message.ToolCalls {
			if call.ID == "" || len(call.ID) > 128 || strings.ContainsFunc(call.ID, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) || ids[call.ID] || call.Type != "function" || !allowed[call.Function.Name] || !aiFunctionName.MatchString(call.Function.Name) {
				return empty, bad
			}
			if !validAIProtocolObject([]byte(call.Function.Arguments), 65536) || !validAIToolSignature(call) {
				return empty, bad
			}
			var arguments map[string]json.RawMessage
			if json.Unmarshal([]byte(call.Function.Arguments), &arguments) != nil || arguments == nil {
				return empty, bad
			}
			ids[call.ID] = true
		}
	case "length", "content_filter", "insufficient_system_resource", "aborted":
		return empty, &AIProviderError{Code: "incomplete_response"}
	default:
		return empty, bad
	}
	return AICompletion{Message: message, FinishReason: choice.FinishReason, PromptTokens: response.Usage.PromptTokens, CompletionTokens: response.Usage.CompletionTokens}, nil
}

func (c *AIClient) Test(ctx context.Context, provider models.AIProvider) error {
	// Reasoning-enabled providers count hidden reasoning toward the output
	// budget. A 32-token probe can truncate before producing even "OK".
	limit := 1024
	if provider.Kind == "deepseek" {
		limit = 32 // This adapter explicitly disables DeepSeek thinking.
	}
	_, err := c.complete(ctx, provider, []AIMessage{{Role: "user", Content: "Reply with the single word OK."}}, nil, limit)
	return err
}
