package services

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"server-monitor/internal/models"
)

func aiTestProvider() models.AIProvider {
	return models.AIProvider{Name: "test", Kind: "compatible", BaseURL: "https://example.com/v1", Model: "fixture-model", APIKey: "fixture-private-key", Enabled: true}
}
func aiTestTools() []AIToolDefinition {
	return []AIToolDefinition{{Type: "function", Function: AIFunctionDefinition{Name: "inspect_host", Description: "Read host status", Parameters: map[string]any{"type": "object"}}}}
}

const aiStopped = `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"OK"}}],"usage":{"prompt_tokens":4,"completion_tokens":1}}`
const aiToolCompletion = `{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"reasoning_content":"continuation-private-state","tool_calls":[{"id":"call-1","type":"function","function":{"name":"inspect_host","arguments":"{\"server\":\"one\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":6}}`

// Test-only networking redirects the approved public IP to an isolated TLS
// fixture while preserving HTTP Host and TLS verification. Production has no
// localhost bypass, environment proxy, custom root CA, or alternate endpoint.
func localAIClient(t *testing.T, handler http.Handler) (*AIClient, *atomic.Int32) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	client := NewAIClient(false)
	calls := &atomic.Int32{}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := client.client.Transport.(*http.Transport)
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	client.lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		if host != "example.com" {
			t.Errorf("unexpected DNS host %q", host)
		}
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	client.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		if address != "93.184.216.34:443" {
			t.Errorf("did not dial pinned public IP: %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, endpoint.Host)
	}
	return client, calls
}

func TestAIProviderEndpointComposition(t *testing.T) {
	for base, want := range map[string]string{
		"https://example.com":                     "https://example.com/chat/completions",
		"https://example.com/":                    "https://example.com/chat/completions",
		"https://example.com/v1":                  "https://example.com/v1/chat/completions",
		"https://example.com/v1/":                 "https://example.com/v1/chat/completions",
		"https://example.com/custom/v2":           "https://example.com/custom/v2/chat/completions",
		"https://example.com/v1/chat/completions": "https://example.com/v1/chat/completions",
	} {
		got, err := aiCompletionEndpoint(base)
		if err != nil || got != want {
			t.Fatalf("%s -> %s %v", base, got, err)
		}
	}
}

func TestAIProviderCompleteAndToolContinuationOverLocalTLS(t *testing.T) {
	var requestCount atomic.Int32
	client, calls := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture-private-key" {
			t.Error("invalid request destination/auth")
		}
		var body struct {
			Model      string             `json:"model"`
			Messages   []AIMessage        `json:"messages"`
			Tools      []AIToolDefinition `json:"tools"`
			ToolChoice string             `json:"tool_choice"`
			Thinking   json.RawMessage    `json:"thinking"`
			Stream     bool               `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "fixture-model" || body.ToolChoice != "auto" || len(body.Tools) != 1 || body.Stream || len(body.Thinking) != 0 {
			t.Errorf("wrong compatible options: %+v", body)
		}
		if requestCount.Add(1) == 1 {
			_, _ = io.WriteString(w, aiToolCompletion)
		} else {
			if len(body.Messages) != 3 || body.Messages[1].ReasoningContent != "continuation-private-state" || body.Messages[1].ToolCalls[0].ID != "call-1" || body.Messages[2].ToolCallID != "call-1" {
				t.Error("continuation/tool context was lost")
			}
			_, _ = io.WriteString(w, aiStopped)
		}
	}))
	messages := []AIMessage{{Role: "user", Content: "Inspect the host"}}
	first, err := client.Complete(context.Background(), aiTestProvider(), messages, aiTestTools())
	if err != nil || first.FinishReason != "tool_calls" || first.Message.Content != "" || len(first.Message.ToolCalls) != 1 || first.PromptTokens != 10 || first.CompletionTokens != 6 {
		t.Fatal("invalid tool completion", first, err)
	}
	messages = append(messages, first.Message, AIMessage{Role: "tool", Content: "host is healthy", ToolCallID: "call-1"})
	final, err := client.Complete(context.Background(), aiTestProvider(), messages, aiTestTools())
	if err != nil || final.Message.Content != "OK" || final.FinishReason != "stop" || calls.Load() != 2 {
		t.Fatal("continuation failed", final, err, calls.Load())
	}
}

func TestDeepSeekDisablesThinkingAndTestReturnsOnlyValidatedSuccess(t *testing.T) {
	client, _ := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(body["thinking"], map[string]any{"type": "disabled"}) || body["max_tokens"] != float64(32) {
			t.Error("DeepSeek test options missing", body)
		}
		if _, exists := body["tools"]; exists {
			t.Error("test request has executable tools")
		}
		_, _ = io.WriteString(w, aiStopped)
	}))
	provider := aiTestProvider()
	provider.Kind = "deepseek"
	if err := client.Test(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
}

func TestAIProviderRejectsIncompleteAndAmbiguousToolResponses(t *testing.T) {
	allowed := map[string]bool{"inspect_host": true}
	cases := []string{
		`{"error":{"message":"fixture-private-key"}}`,
		`{"choices":[]}`,
		strings.Replace(aiToolCompletion, `"tool_calls","message"`, `"stop","message"`, 1),
		strings.Replace(aiToolCompletion, `"tool_calls","message"`, `"length","message"`, 1),
		strings.Replace(aiToolCompletion, `"tool_calls","message"`, `"content_filter","message"`, 1),
		strings.Replace(aiToolCompletion, `"tool_calls","message"`, `"aborted","message"`, 1),
		strings.Replace(aiToolCompletion, `"tool_calls","message"`, `"unknown","message"`, 1),
		strings.Replace(aiToolCompletion, `"assistant"`, `"system"`, 1),
		strings.Replace(aiToolCompletion, `"type":"function"`, `"type":"shell"`, 1),
		strings.Replace(aiToolCompletion, `"inspect_host"`, `"unlisted_function"`, 1),
		strings.Replace(aiToolCompletion, `"id":"call-1"`, `"id":""`, 1),
		strings.Replace(aiToolCompletion, `"arguments":"{\"server\":\"one\"}"`, `"arguments":"[]"`, 1),
		strings.Replace(aiToolCompletion, `"arguments":"{\"server\":\"one\"}"`, `"arguments":"null"`, 1),
		strings.Replace(aiToolCompletion, `"arguments":"{\"server\":\"one\"}"`, `"arguments":"{bad"`, 1),
		strings.Replace(aiToolCompletion, `"content":null`, `"content":[{"text":"not supported"}]`, 1),
		strings.Replace(aiStopped, `"content":"OK"`, `"content":null`, 1),
		strings.Replace(aiStopped, `"prompt_tokens":4`, `"prompt_tokens":-1`, 1),
		aiStopped + ` {"choices":[]}`,
	}
	var repeated map[string]any
	_ = json.Unmarshal([]byte(aiToolCompletion), &repeated)
	message := repeated["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	message["tool_calls"] = append(message["tool_calls"].([]any), message["tool_calls"].([]any)[0])
	duplicate, _ := json.Marshal(repeated)
	cases = append(cases, string(duplicate))
	for _, body := range cases {
		if _, err := parseAICompletion([]byte(body), allowed); err == nil {
			t.Fatalf("invalid response accepted: %s", body)
		}
	}
}

func TestAIProviderHTTPFailuresAreSafeAndNotRetried(t *testing.T) {
	for _, status := range []int{301, 307, 401, 403, 429, 500, 400} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client, calls := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://attacker.example/collect")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"fixture-private-key private URL"}`)
			}))
			_, err := client.Complete(context.Background(), aiTestProvider(), []AIMessage{{Role: "user", Content: "test"}}, nil)
			code, message := SafeAIProviderError(err)
			if err == nil || calls.Load() != 1 || strings.Contains(message, "fixture-private-key") || strings.Contains(message, "URL") {
				t.Fatal("failure retried or leaked", err, calls.Load(), message)
			}
			if (status == 401 || status == 403) && code != "authentication_failed" {
				t.Fatal("wrong authentication category", code)
			}
			if status >= 300 && status < 400 && code != "redirect_rejected" {
				t.Fatal("redirect not rejected", code)
			}
		})
	}
}

func TestAIProviderBodyLimitAndCancellation(t *testing.T) {
	t.Run("body limit", func(t *testing.T) {
		client, calls := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, strings.Repeat("x", AIProviderBodyLimit+100))
		}))
		_, err := client.Complete(context.Background(), aiTestProvider(), []AIMessage{{Role: "user", Content: "test"}}, nil)
		code, _ := SafeAIProviderError(err)
		if code != "response_too_large" || calls.Load() != 1 {
			t.Fatal(code, err, calls.Load())
		}
	})
	t.Run("cancel response", func(t *testing.T) {
		started := make(chan struct{})
		released := make(chan struct{})
		client, calls := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			select {
			case <-r.Context().Done():
			case <-released:
			}
		}))
		defer close(released)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := client.Complete(ctx, aiTestProvider(), []AIMessage{{Role: "user", Content: "test"}}, nil)
			done <- err
		}()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("request did not begin")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal("cancel lost", err)
			}
		case <-time.After(time.Second):
			t.Fatal("canceled request hung")
		}
		if calls.Load() != 1 {
			t.Fatal("canceled request retried")
		}
	})
	t.Run("cancel DNS", func(t *testing.T) {
		client := NewAIClient(false)
		client.lookupIP = func(ctx context.Context, _ string) ([]net.IP, error) { <-ctx.Done(); return nil, ctx.Err() }
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		_, err := client.Complete(ctx, aiTestProvider(), []AIMessage{{Role: "user", Content: "test"}}, nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("DNS ignores caller deadline", err)
		}
	})
}

func TestAIProviderDNSPolicyAndPinning(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1", "240.0.0.1", "192.0.2.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2002:7f00:1::", "2001:db8::1"} {
		if aiPublicIP(net.ParseIP(ip)) {
			t.Fatal("restricted IP accepted", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "93.184.216.34", "2606:4700:4700::1111"} {
		if !aiPublicIP(net.ParseIP(ip)) {
			t.Fatal("public IP rejected", ip)
		}
	}
	client := NewAIClient(false)
	transport := client.client.Transport.(*http.Transport)
	if transport.Proxy != nil || !transport.DisableKeepAlives || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("unsafe AI HTTP transport")
	}
	client.lookupIP = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("127.0.0.1")}, nil
	}
	if err := client.ValidateEndpoint(context.Background(), "https://example.com/v1"); err == nil {
		t.Fatal("mixed DNS accepted")
	}
	client.allowPrivate = true
	if err := client.ValidateEndpoint(context.Background(), "https://example.com/v1"); err != nil {
		t.Fatal("explicit private endpoint policy ignored", err)
	}
	if err := client.ValidateEndpoint(context.Background(), "http://localhost"); err == nil {
		t.Fatal("private policy bypasses HTTPS")
	}
	if _, err := client.dialPinned(context.Background(), "tcp", "example.com:443"); err == nil {
		t.Fatal("unvalidated dial accepted")
	}
	pin := aiPinnedEndpoint{Host: "example.com", Port: "443", IPs: []net.IP{net.ParseIP("8.8.8.8")}}
	ctx := context.WithValue(context.Background(), aiPinnedEndpointKey{}, pin)
	if _, err := client.dialPinned(ctx, "tcp", "other.example:443"); err == nil {
		t.Fatal("pin reused for different host")
	}
	if _, err := client.dialPinned(ctx, "tcp", "example.com:8443"); err == nil {
		t.Fatal("pin reused for different port")
	}
}

func TestAIProviderInputLimitsAndDisabledPreventOutboundCalls(t *testing.T) {
	client := NewAIClient(false)
	var resolutions int
	client.lookupIP = func(context.Context, string) ([]net.IP, error) {
		resolutions++
		return nil, errors.New("must not resolve")
	}
	provider := aiTestProvider()
	provider.Enabled = false
	if _, err := client.Complete(context.Background(), provider, []AIMessage{{Role: "user", Content: "test"}}, nil); err == nil {
		t.Fatal("disabled provider accepted")
	}
	provider.Enabled = true
	for _, messages := range [][]AIMessage{nil, {{Role: "invalid", Content: "test"}}, {{Role: "user", Content: strings.Repeat("x", aiRequestBodyLimit+1)}}, {{Role: "tool", Content: "no matching tool ID"}}} {
		if _, err := client.Complete(context.Background(), provider, messages, nil); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if resolutions != 0 {
		t.Fatal("invalid or disabled request resolved DNS")
	}
}

func TestAIProviderRevalidatesDNSBetweenRequestsAndVerifiesTLS(t *testing.T) {
	t.Run("DNS rebind", func(t *testing.T) {
		client, calls := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, aiStopped) }))
		var resolutions int
		client.lookupIP = func(context.Context, string) ([]net.IP, error) {
			resolutions++
			if resolutions == 1 {
				return []net.IP{net.ParseIP("93.184.216.34")}, nil
			}
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		messages := []AIMessage{{Role: "user", Content: "test"}}
		if _, err := client.Complete(context.Background(), aiTestProvider(), messages, nil); err != nil {
			t.Fatal(err)
		}
		_, err := client.Complete(context.Background(), aiTestProvider(), messages, nil)
		code, _ := SafeAIProviderError(err)
		if code != "endpoint_restricted" || calls.Load() != 1 || resolutions != 2 {
			t.Fatal("stale connection bypassed changed DNS", code, calls.Load(), resolutions)
		}
	})
	t.Run("untrusted certificate", func(t *testing.T) {
		var requests atomic.Int32
		client, _ := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = io.WriteString(w, aiStopped) }))
		client.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = x509.NewCertPool()
		_, err := client.Complete(context.Background(), aiTestProvider(), []AIMessage{{Role: "user", Content: "test"}}, nil)
		code, _ := SafeAIProviderError(err)
		if code != "connection_failed" || requests.Load() != 0 {
			t.Fatal("untrusted server received key", code, requests.Load())
		}
	})
}
