package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

const (
	openRouterAPIBaseURL = "https://openrouter.ai/api/v1"
	openRouterModelsURL  = openRouterAPIBaseURL + "/models"
	cacheTTL             = 24 * time.Hour // Cache models for 24 hours
)

var (
	openRouterCache     = make(map[string]*cachedModelMetadata)
	openRouterCacheMu   sync.RWMutex
	openRouterCacheTime time.Time
)

type cachedModelMetadata struct {
	metadata *llmtypes.ModelMetadata
	expires  time.Time
}

// OpenRouterModelsResponse represents the response from OpenRouter's Models API
type OpenRouterModelsResponse struct {
	Data []OpenRouterModel `json:"data"`
}

// OpenRouterModel represents a single model from OpenRouter's API
type OpenRouterModel struct {
	ID            string                `json:"id"`
	CanonicalSlug string                `json:"canonical_slug"`
	Name          string                `json:"name"`
	ContextLength int                   `json:"context_length"`
	Pricing       OpenRouterPricing     `json:"pricing"`
	TopProvider   OpenRouterTopProvider `json:"top_provider"`
}

// OpenRouterPricing represents pricing information from OpenRouter
type OpenRouterPricing struct {
	Prompt            string `json:"prompt"`             // Cost per input token (as string)
	Completion        string `json:"completion"`         // Cost per output token (as string)
	Request           string `json:"request"`            // Fixed cost per request
	Image             string `json:"image"`              // Cost per image
	WebSearch         string `json:"web_search"`         // Cost per web search
	InternalReasoning string `json:"internal_reasoning"` // Cost per reasoning token
	InputCacheRead    string `json:"input_cache_read"`   // Cost per cached input token read
	InputCacheWrite   string `json:"input_cache_write"`  // Cost per cached input token write
}

// OpenRouterTopProvider represents top provider information
type OpenRouterTopProvider struct {
	ContextLength       int  `json:"context_length"`
	MaxCompletionTokens int  `json:"max_completion_tokens"`
	IsModerated         bool `json:"is_moderated"`
}

// fetchOpenRouterModels fetches the list of models from OpenRouter's API
func fetchOpenRouterModels() (*OpenRouterModelsResponse, error) {
	req, err := http.NewRequestWithContext(context.Background(), "GET", openRouterModelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OpenRouter models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OpenRouter API returned status %d: %s", resp.StatusCode, string(body))
	}

	var modelsResp OpenRouterModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return nil, fmt.Errorf("failed to decode OpenRouter models response: %w", err)
	}

	return &modelsResp, nil
}

// parsePriceString converts a price string (e.g., "0.000001" per token) to cost per 1M tokens
func parsePriceString(priceStr string) float64 {
	if priceStr == "" || priceStr == "0" {
		return 0.0
	}

	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		return 0.0
	}

	// Convert from per-token to per-1M-tokens
	return price * 1_000_000
}

// convertOpenRouterModelToMetadata converts an OpenRouter model to our ModelMetadata format
func convertOpenRouterModelToMetadata(model OpenRouterModel) *llmtypes.ModelMetadata {
	// Use top_provider context_length if available, otherwise use model context_length
	contextWindow := model.ContextLength
	if model.TopProvider.ContextLength > 0 {
		contextWindow = model.TopProvider.ContextLength
	}

	// Extract provider from model ID (format: "provider/model-name")
	provider := "openrouter"
	if parts := strings.Split(model.ID, "/"); len(parts) > 0 {
		provider = parts[0]
	}

	return &llmtypes.ModelMetadata{
		ModelID:                    model.ID,
		ModelName:                  model.Name,
		ContextWindow:              contextWindow,
		InputCostPer1MTokens:       parsePriceString(model.Pricing.Prompt),
		OutputCostPer1MTokens:      parsePriceString(model.Pricing.Completion),
		ReasoningCostPer1MTokens:   parsePriceString(model.Pricing.InternalReasoning),
		CachedInputCostPer1MTokens: parsePriceString(model.Pricing.InputCacheRead),
		Provider:                   provider,
	}
}

// getOpenRouterModelsFromAPI fetches and caches OpenRouter models
func getOpenRouterModelsFromAPI() (map[string]*llmtypes.ModelMetadata, error) {
	openRouterCacheMu.RLock()
	// Check if cache is still valid
	if time.Since(openRouterCacheTime) < cacheTTL && len(openRouterCache) > 0 {
		// Build metadata map from cache
		metadataMap := make(map[string]*llmtypes.ModelMetadata)
		for id, cached := range openRouterCache {
			if time.Now().Before(cached.expires) {
				metadataMap[id] = cached.metadata
			}
		}
		openRouterCacheMu.RUnlock()
		if len(metadataMap) > 0 {
			return metadataMap, nil
		}
	}
	openRouterCacheMu.RUnlock()

	// Cache expired or empty, fetch from API
	openRouterCacheMu.Lock()
	defer openRouterCacheMu.Unlock()

	// Double-check after acquiring write lock
	if time.Since(openRouterCacheTime) < cacheTTL && len(openRouterCache) > 0 {
		metadataMap := make(map[string]*llmtypes.ModelMetadata)
		for id, cached := range openRouterCache {
			if time.Now().Before(cached.expires) {
				metadataMap[id] = cached.metadata
			}
		}
		if len(metadataMap) > 0 {
			return metadataMap, nil
		}
	}

	// Fetch from API
	modelsResp, err := fetchOpenRouterModels()
	if err != nil {
		return nil, err
	}

	// Convert and cache all models
	metadataMap := make(map[string]*llmtypes.ModelMetadata)
	newCache := make(map[string]*cachedModelMetadata)
	expires := time.Now().Add(cacheTTL)

	for _, model := range modelsResp.Data {
		metadata := convertOpenRouterModelToMetadata(model)
		metadataMap[model.ID] = metadata
		newCache[model.ID] = &cachedModelMetadata{
			metadata: metadata,
			expires:  expires,
		}
	}

	// Update cache
	openRouterCache = newCache
	openRouterCacheTime = time.Now()

	return metadataMap, nil
}

// GetOpenRouterModelMetadata retrieves metadata for an OpenRouter model by fetching from the API
// It caches results for 24 hours to avoid excessive API calls
func GetOpenRouterModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	if modelID == "" {
		return nil, fmt.Errorf("model ID cannot be empty")
	}

	// Check if this is an OpenRouter model ID (contains "/")
	if !strings.Contains(modelID, "/") {
		return nil, fmt.Errorf("model ID does not appear to be an OpenRouter model (expected format: provider/model-name)")
	}

	// Get models from API (with caching)
	models, err := getOpenRouterModelsFromAPI()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OpenRouter models: %w", err)
	}

	// Look up the model
	metadata, found := models[modelID]
	if !found {
		return nil, fmt.Errorf("model not found in OpenRouter: %s", modelID)
	}

	return metadata, nil
}
