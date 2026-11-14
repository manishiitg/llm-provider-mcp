package llmtypes

// ProviderRetry is provider-owned retry progress, not a request to replay the
// original prompt. Hosts render it using their existing retry event.
type ProviderRetry struct {
	Provider             string
	Attempt, MaxAttempts int
	DelayMS              int64
}

const StreamChunkTypeProviderRetry StreamChunkType = "provider_retry"
