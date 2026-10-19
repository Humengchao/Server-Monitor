package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAIModelsEndpointComposition(t *testing.T) {
	for base, want := range map[string]string{
		"https://example.com":                      "https://example.com/models",
		"https://example.com/v1/":                  "https://example.com/v1/models",
		"https://example.com/custom/api/v2":        "https://example.com/custom/api/v2/models",
		"https://example.com/v1/chat/completions/": "https://example.com/v1/models",
	} {
		got, err := aiModelsEndpoint(base)
		if err != nil || got != want {
			t.Fatalf("%s -> %s %v", base, got, err)
		}
	}
}

func TestAIModelsFreshAuthenticatedSortedCatalog(t *testing.T) {
	var requests int
	client, calls := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "GET" || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer fixture-private-key" {
			t.Error("invalid model-list destination or authentication")
		}
		if requests == 1 {
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"z-model","owned_by":"fixture","secret":"ignored"},{"id":"a-model"},{"id":"z-model"}]}`)
		} else {
			_, _ = io.WriteString(w, `{"data":[{"id":"brand-new-model"}]}`)
		}
	}))
	provider := aiTestProvider()
	provider.Model = "" // Initial configuration can discover before selecting.
	first, err := client.ListModels(context.Background(), provider)
	if err != nil || !reflect.DeepEqual(first, []AIAvailableModel{{ID: "a-model"}, {ID: "z-model", OwnedBy: "fixture"}}) {
		t.Fatalf("invalid first catalog: %+v %v", first, err)
	}
	second, err := client.ListModels(context.Background(), provider)
	if err != nil || len(second) != 1 || second[0].ID != "brand-new-model" || calls.Load() != 2 || provider.Model != "" {
		t.Fatalf("catalog was cached or altered selected model: %+v %v", second, err)
	}
}

func TestAIModelsRejectsUnsafeAndMalformedMetadata(t *testing.T) {
	invalid := []string{`{}`, `{"data":null}`, `{"data":{}}`, `{"data":[{}]}`, `{"data":[{"id":"white space"}]}`, `{"data":[{"id":"a\u0000b"}]}`, `{"data":[{"id":"good","ID":"other"}]}`, `{"data":[{"id":"good","owned_by":4}]}`, `{"data":[],"error":{"message":"secret"}}`, `{"data":[{"id":"fixture-private-key"}]}`, `{"data":[{"id":"fine","owned_by":"fixture-private-key"}]}`, `{"data":[{"id":"` + strings.Repeat("a", 129) + `"}]}`}
	for _, raw := range invalid {
		_, err := parseAIModels([]byte(raw), "fixture-private-key")
		code, _ := SafeAIProviderError(err)
		if code != "invalid_models_response" {
			t.Fatalf("invalid catalog accepted: %s %v", raw, err)
		}
	}
	result, err := parseAIModels([]byte(`{"data":[]}`), "fixture-private-key")
	if err != nil || result == nil || len(result) != 0 {
		t.Fatal("empty catalog must be an explicit empty list", result, err)
	}
}

func TestAIModelsRejectsEncodedCredentialMetadata(t *testing.T) {
	// A compatible gateway may wrap its diagnostics in JSON strings. A single
	// decode must not make the stored API key available through model discovery.
	key := "fixture-private-key"
	encoded := `"\u0066ixture-private-key"`
	for _, field := range []string{"id", "owned_by"} {
		t.Run(field, func(t *testing.T) {
			model := map[string]string{"id": "safe-model"}
			model[field] = encoded
			raw, err := json.Marshal(map[string]any{"data": []any{model}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := parseAIModels(raw, key)
			if err == nil || result != nil {
				t.Fatal("encoded saved credential was returned in model metadata")
			}
		})
	}
}

func TestAIModelsUpstreamErrorsNeverEchoBodies(t *testing.T) {
	for _, fixture := range []struct {
		status int
		code   string
	}{{301, "redirect_rejected"}, {401, "authentication_failed"}, {403, "authentication_failed"}, {404, "models_not_supported"}, {405, "models_not_supported"}, {501, "models_not_supported"}, {429, "rate_limited"}, {500, "upstream_unavailable"}} {
		t.Run(fixture.code, func(t *testing.T) {
			client, calls := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://127.0.0.1/steal-key")
				w.WriteHeader(fixture.status)
				_, _ = io.WriteString(w, "fixture-private-key: sensitive upstream error")
			}))
			_, err := client.ListModels(context.Background(), aiTestProvider())
			code, safe := SafeAIProviderError(err)
			if code != fixture.code || strings.Contains(safe, "fixture-private-key") || calls.Load() != 1 {
				t.Fatal("unsafe error or retry", code, safe, calls.Load())
			}
		})
	}
}

func TestAIModelsDNSRebindingRejectedBeforeSecondConnection(t *testing.T) {
	client, calls := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	if _, err := client.ListModels(context.Background(), aiTestProvider()); err != nil {
		t.Fatal(err)
	}
	client.lookupIP = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	_, err := client.ListModels(context.Background(), aiTestProvider())
	code, _ := SafeAIProviderError(err)
	if code != "endpoint_restricted" || calls.Load() != 1 {
		t.Fatal("rebound models endpoint dialed", err)
	}
}

func TestAIModelsCancellationAndBodyLimit(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		entered := make(chan struct{})
		client, _ := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
		}))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := client.ListModels(ctx, aiTestProvider()); done <- err }()
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("request did not start")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("model discovery ignored cancellation")
		}
	})
	t.Run("body", func(t *testing.T) {
		client, _ := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", AIProviderBodyLimit+1))
		}))
		_, err := client.ListModels(context.Background(), aiTestProvider())
		code, _ := SafeAIProviderError(err)
		if code != "response_too_large" {
			t.Fatal(err)
		}
	})
}

func TestOpenAIUsesCompletionTokenLimit(t *testing.T) {
	client, _ := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, exists := body["max_tokens"]; exists || string(body["max_completion_tokens"]) != "4096" {
			t.Error("OpenAI sent incompatible token limit")
		}
		_, _ = io.WriteString(w, aiStopped)
	}))
	provider := aiTestProvider()
	provider.Kind = "openai"
	if _, err := client.Complete(context.Background(), provider, []AIMessage{{Role: "user", Content: "hello"}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestGeminiToolThoughtSignatureSurvivesContinuation(t *testing.T) {
	var requests int
	client, _ := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			_, _ = io.WriteString(w, strings.Replace(aiToolCompletion, `"id":"call-1"`, `"extra_content":{"google":{"thought_signature":"opaque-signature+/="}},"id":"call-1"`, 1))
			return
		}
		var body struct {
			Messages []AIMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 3 || aiToolSignature(body.Messages[1].ToolCalls[0]) != "opaque-signature+/=" {
			t.Error("thought signature lost")
		}
		_, _ = io.WriteString(w, aiStopped)
	}))
	provider := aiTestProvider()
	provider.Kind = "gemini"
	messages := []AIMessage{{Role: "user", Content: "inspect"}}
	first, err := client.Complete(context.Background(), provider, messages, aiTestTools())
	if err != nil {
		t.Fatal(err)
	}
	// Persist and reload exactly as the worker does across approval/restart.
	stored, err := json.Marshal(first.Message)
	if err != nil {
		t.Fatal(err)
	}
	var restored AIMessage
	if err = json.Unmarshal(stored, &restored); err != nil {
		t.Fatal(err)
	}
	messages = append(messages, restored, AIMessage{Role: "tool", ToolCallID: "call-1", Content: "healthy"})
	if _, err = client.Complete(context.Background(), provider, messages, aiTestTools()); err != nil {
		t.Fatal(err)
	}
}

func TestAIThoughtSignatureBoundsAndReasoningProbeBudget(t *testing.T) {
	for _, signature := range []string{strings.Repeat("x", 65537), "line\nbreak", "non-ascii-密钥"} {
		extra, _ := json.Marshal(AIToolExtraContent{Google: &AIGoogleToolContent{ThoughtSignature: signature}})
		raw := strings.Replace(aiToolCompletion, `"id":"call-1"`, `"extra_content":`+string(extra)+`,"id":"call-1"`, 1)
		if _, err := parseAICompletion([]byte(raw), map[string]bool{"inspect_host": true}); err == nil {
			t.Fatal("unbounded thought signature accepted")
		}
	}
	client, _ := localAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if string(body["max_completion_tokens"]) != "1024" || len(body["tools"]) != 0 {
			t.Error("reasoning probe lacks a usable budget or exposes execution tools")
		}
		_, _ = io.WriteString(w, aiStopped)
	}))
	provider := aiTestProvider()
	provider.Kind = "openai"
	if err := client.Test(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
}
