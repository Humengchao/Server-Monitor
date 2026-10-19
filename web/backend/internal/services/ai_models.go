package services

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"unicode"

	"server-monitor/internal/models"
)

// AIAvailableModel deliberately exposes only catalog identifiers, never an
// upstream raw payload or capabilities that could be mistaken for permission.
type AIAvailableModel struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by,omitempty"`
}

func aiModelsEndpoint(baseURL string) (string, error) {
	parsed, err := models.ParseAIBaseURL(baseURL)
	if err != nil {
		return "", &AIProviderError{Code: "invalid_configuration"}
	}
	path := strings.TrimRight(parsed.Path, "/")
	// Full chat-completion URLs remain supported for existing configurations.
	path = strings.TrimSuffix(path, "/chat/completions")
	parsed.Path = path + "/models"
	return parsed.String(), nil
}

// ListModels is a fresh authenticated read, without changing the provider's
// selected model. It also accepts preview configurations without a model ID.
func (c *AIClient) ListModels(ctx context.Context, provider models.AIProvider) ([]AIAvailableModel, error) {
	if !provider.Enabled {
		return nil, &AIProviderError{Code: "provider_disabled"}
	}
	provider.Model = "model-catalog"
	if err := models.NormalizeAIProvider(&provider); err != nil || provider.APIKey == "" {
		return nil, &AIProviderError{Code: "invalid_configuration"}
	}
	endpoint, err := aiModelsEndpoint(provider.BaseURL)
	if err != nil {
		return nil, err
	}
	raw, err := c.requestJSON(ctx, provider, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	return parseAIModels(raw, provider.APIKey)
}

func parseAIModels(raw []byte, apiKey string) ([]AIAvailableModel, error) {
	bad := &AIProviderError{Code: "invalid_models_response"}
	var response struct {
		Data  []AIAvailableModel `json:"data"`
		Error json.RawMessage    `json:"error"`
	}
	if !validAIProtocolObject(raw, AIProviderBodyLimit) || json.Unmarshal(raw, &response) != nil || response.Data == nil || len(response.Data) > 10000 || len(response.Error) > 0 && string(response.Error) != "null" {
		return nil, bad
	}
	result := make([]AIAvailableModel, 0, len(response.Data))
	seen := make(map[string]bool, len(response.Data))
	for _, model := range response.Data {
		if model.ID == "" || len(model.ID) > 128 || strings.ContainsFunc(model.ID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || len(model.OwnedBy) > 256 || strings.ContainsFunc(model.OwnedBy, unicode.IsControl) {
			return nil, bad
		}
		// A gateway may accidentally echo credentials in metadata. Do not turn
		// model discovery into a route that returns a saved key to the browser.
		if apiKey != "" && (strings.Contains(model.ID, apiKey) || strings.Contains(model.OwnedBy, apiKey)) {
			return nil, bad
		}
		if !seen[model.ID] {
			seen[model.ID] = true
			result = append(result, model)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
