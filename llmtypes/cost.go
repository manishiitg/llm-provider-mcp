package llmtypes

// ComputeUSDCostFromMetadata returns the API-equivalent USD cost given
// a ModelMetadata entry and token counts from a GenerationInfo. Pure
// math, no registry lookup — callers must resolve the model metadata
// themselves (typically via their adapter's GetModelMetadata).
//
// Returns 0 when either argument is nil, when no tokens were recorded,
// or when the metadata has zero rates — cost is best-effort and a
// missing answer should not break the response path.
//
// For subscription-based CLIs (Cursor, Codex Pro) the result is a
// SHADOW cost: what the same workload would cost via the underlying
// per-token API, NOT what the user is billed for the flat plan.
func ComputeUSDCostFromMetadata(meta *ModelMetadata, gi *GenerationInfo) float64 {
	if meta == nil || gi == nil {
		return 0
	}
	prompt := firstNonZeroIntPtr(gi.PromptTokens, gi.InputTokens, gi.PromptTokensCap, gi.InputTokensCap)
	completion := firstNonZeroIntPtr(gi.CompletionTokens, gi.OutputTokens, gi.CompletionTokensCap, gi.OutputTokensCap)
	cached := firstNonZeroIntPtr(gi.CachedContentTokens)
	if prompt == 0 && completion == 0 && cached == 0 {
		return 0
	}

	var cost float64
	cost += float64(prompt) * meta.InputCostPer1MTokens / 1_000_000
	cost += float64(completion) * meta.OutputCostPer1MTokens / 1_000_000
	if cached > 0 {
		rate := meta.CachedInputCostPer1MTokens
		if rate == 0 {
			// Most providers charge ~10% of the input rate on cache reads
			// (Anthropic, OpenAI prompt caching). Fall back to that
			// convention when the registry doesn't carry an explicit
			// cache rate.
			rate = meta.InputCostPer1MTokens * 0.10
		}
		cost += float64(cached) * rate / 1_000_000
	}
	return cost
}

func firstNonZeroIntPtr(ptrs ...*int) int {
	for _, p := range ptrs {
		if p != nil && *p > 0 {
			return *p
		}
	}
	return 0
}
