package opencodecli

// OpenCodeSubProvider is a logical provider exposed through the OpenCode CLI.
// Each entry maps a user-facing tile (e.g. "Kimi", "DeepSeek") to the
// OpenCode-internal provider id that owns its model namespace, the env var
// OpenCode's bundled SDK looks at for credentials, and a curated subset of
// models that the UI surfaces by default.
//
// Sub-providers are first-class citizens: they get their own tile in the
// configuration UI, their own API key field, and their own model picker.
// They all execute through the same `opencode run` binary but each turn
// injects only the credentials for the selected sub-provider.
type OpenCodeSubProvider struct {
	// ID is the stable identifier used as the manifest provider id
	// (e.g. "opencode-cli-kimi"). It is what the frontend selects.
	ID string

	// OpenCodeProviderID is OpenCode's internal provider namespace as
	// returned by `opencode models` and accepted by `--model`. Full model
	// ids passed to OpenCode are `<OpenCodeProviderID>/<ModelID>`.
	OpenCodeProviderID string

	// DisplayName is the user-facing label rendered on the provider tile.
	DisplayName string

	// Description is a one-line summary rendered under the tile.
	Description string

	// APIKeyEnvVar is the env var the OpenCode bundled SDK reads to
	// authenticate against this provider. Empty for free/hosted providers.
	APIKeyEnvVar string

	// APIKeyURL is the public sign-up / dashboard URL surfaced to the user
	// next to the API-key entry field. Empty for free/hosted providers.
	APIKeyURL string

	// RequiresAPIKey gates whether the UI must collect a credential before
	// the provider can be enabled.
	RequiresAPIKey bool

	// Models is the curated list of models offered for this sub-provider.
	Models []OpenCodeSubProviderModel

	// DefaultModelID is the model id used when the caller asks for "auto"
	// or does not specify one. It is the bare id (without the provider
	// prefix); the adapter prepends OpenCodeProviderID.
	DefaultModelID string

	// TierShortcuts maps "high" / "medium" / "low" to bare model ids in
	// this sub-provider's namespace. Empty values mean "no shortcut for
	// this tier"; callers asking for a missing tier fall back to
	// DefaultModelID.
	TierShortcuts map[string]string
}

// OpenCodeSubProviderModel is a single curated model entry inside a
// sub-provider.
type OpenCodeSubProviderModel struct {
	// ID is the bare model id within the OpenCode provider namespace
	// (e.g. "kimi-k2-thinking"). The full model id passed to OpenCode is
	// constructed as `<OpenCodeProviderID>/<ID>`.
	ID string

	// DisplayName is the user-facing model name.
	DisplayName string

	// Group is the model family used by the UI to organize the model
	// dropdown (e.g. "Kimi K2", "Qwen 3.6", "MiniMax M2").
	Group string

	// ContextWindow is the maximum input window size in tokens, sourced
	// from the provider's published spec via models.dev.
	ContextWindow int

	// CostInput / CostOutput are USD per million input/output tokens.
	// Zero for free models.
	CostInput  float64
	CostOutput float64

	// SupportsTools is whether the model can call tools through OpenCode.
	SupportsTools bool

	// SupportsReasoning is whether the model exposes a chain-of-thought /
	// reasoning channel.
	SupportsReasoning bool

	// IsDefault marks the model OpenCode tile preselects.
	IsDefault bool
}

// FullModelID returns the OpenCode `<provider>/<model>` form that gets
// passed as `--model`.
func (m OpenCodeSubProviderModel) FullModelID(sp OpenCodeSubProvider) string {
	return sp.OpenCodeProviderID + "/" + m.ID
}

// openCodeSubProviders defines every sub-provider exposed as a first-class
// tile. The metadata mirrors models.dev's catalog (context windows, prices)
// and the env vars OpenCode's bundled SDKs already look at, so no
// per-provider code path is needed beyond injecting the right env var at
// launch time.
var openCodeSubProviders = []OpenCodeSubProvider{
	{
		ID:                 "opencode-cli-kimi",
		OpenCodeProviderID: "kimi-for-coding",
		DisplayName:        "Kimi (For Coding)",
		Description:        "Moonshot's coder-tuned API for Kimi K2. Routes through OpenCode so the assistant identifies as an allowed coding agent.",
		APIKeyEnvVar:       "KIMI_API_KEY",
		APIKeyURL:          "https://www.kimi.com/coding/docs/en/third-party-agents.html",
		RequiresAPIKey:     true,
		DefaultModelID:     "kimi-k2-thinking",
		TierShortcuts: map[string]string{
			"high":   "kimi-k2-thinking",
			"medium": "k2p6",
			"low":    "k2p5",
		},
		Models: []OpenCodeSubProviderModel{
			{ID: "kimi-k2-thinking", DisplayName: "Kimi K2 Thinking", Group: "Kimi K2", ContextWindow: 256000, SupportsTools: true, SupportsReasoning: true, IsDefault: true},
			{ID: "k2p6", DisplayName: "Kimi K2.6", Group: "Kimi K2", ContextWindow: 256000, SupportsTools: true},
			{ID: "k2p5", DisplayName: "Kimi K2.5", Group: "Kimi K2", ContextWindow: 256000, SupportsTools: true},
		},
	},
	{
		ID:                 "opencode-cli-deepseek",
		OpenCodeProviderID: "deepseek",
		DisplayName:        "DeepSeek",
		Description:        "DeepSeek's hosted API. Includes the v4 family and the reasoner model.",
		APIKeyEnvVar:       "DEEPSEEK_API_KEY",
		APIKeyURL:          "https://platform.deepseek.com/api_keys",
		RequiresAPIKey:     true,
		DefaultModelID:     "deepseek-v4-pro",
		TierShortcuts: map[string]string{
			"high":   "deepseek-v4-pro",
			"medium": "deepseek-reasoner",
			"low":    "deepseek-v4-flash",
		},
		Models: []OpenCodeSubProviderModel{
			{ID: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro", Group: "DeepSeek V4", ContextWindow: 128000, SupportsTools: true, IsDefault: true},
			{ID: "deepseek-chat", DisplayName: "DeepSeek Chat", Group: "DeepSeek V4", ContextWindow: 128000, SupportsTools: true},
			{ID: "deepseek-reasoner", DisplayName: "DeepSeek Reasoner", Group: "DeepSeek V4", ContextWindow: 128000, SupportsTools: true, SupportsReasoning: true},
			{ID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash", Group: "DeepSeek V4", ContextWindow: 128000, SupportsTools: true},
		},
	},
	{
		ID:                 "opencode-cli-qwen",
		OpenCodeProviderID: "alibaba",
		DisplayName:        "Qwen (Alibaba)",
		Description:        "Alibaba's DashScope-hosted Qwen models, including Qwen 3.6 Plus, Qwen 3 Coder Next, and Qwen 3 Max.",
		APIKeyEnvVar:       "DASHSCOPE_API_KEY",
		APIKeyURL:          "https://dashscope-intl.console.alibabacloud.com/apiKey",
		RequiresAPIKey:     true,
		DefaultModelID:     "qwen3.6-plus",
		TierShortcuts: map[string]string{
			"high":   "qwen3-max-2026-01-23",
			"medium": "qwen3.6-plus",
			"low":    "qwen3.5-plus",
		},
		Models: []OpenCodeSubProviderModel{
			{ID: "qwen3.6-plus", DisplayName: "Qwen 3.6 Plus", Group: "Qwen 3.6", ContextWindow: 131072, SupportsTools: true, IsDefault: true},
			{ID: "qwen3-coder-next", DisplayName: "Qwen 3 Coder Next", Group: "Qwen 3 Coder", ContextWindow: 131072, SupportsTools: true},
			{ID: "qwen3.5-plus", DisplayName: "Qwen 3.5 Plus", Group: "Qwen 3.5", ContextWindow: 131072, SupportsTools: true},
			{ID: "qwen3-max-2026-01-23", DisplayName: "Qwen 3 Max", Group: "Qwen 3 Max", ContextWindow: 131072, SupportsTools: true},
		},
	},
	{
		ID:                 "opencode-cli-minimax",
		OpenCodeProviderID: "minimax",
		DisplayName:        "MiniMax",
		Description:        "MiniMax M2 family via the Anthropic-compatible endpoint.",
		APIKeyEnvVar:       "MINIMAX_API_KEY",
		APIKeyURL:          "https://www.minimax.io/platform/user-center/basic-information/interface-key",
		RequiresAPIKey:     true,
		DefaultModelID:     "MiniMax-M2.5",
		TierShortcuts: map[string]string{
			"high":   "MiniMax-M2.7",
			"medium": "MiniMax-M2.5",
			"low":    "MiniMax-M2.5-highspeed",
		},
		Models: []OpenCodeSubProviderModel{
			{ID: "MiniMax-M2.7", DisplayName: "MiniMax M2.7", Group: "MiniMax M2", ContextWindow: 200000, SupportsTools: true},
			{ID: "MiniMax-M2.5", DisplayName: "MiniMax M2.5", Group: "MiniMax M2", ContextWindow: 200000, SupportsTools: true, IsDefault: true},
			{ID: "MiniMax-M2.1", DisplayName: "MiniMax M2.1", Group: "MiniMax M2", ContextWindow: 200000, SupportsTools: true},
			{ID: "MiniMax-M2", DisplayName: "MiniMax M2", Group: "MiniMax M2", ContextWindow: 200000, SupportsTools: true},
			{ID: "MiniMax-M2.7-highspeed", DisplayName: "MiniMax M2.7 Highspeed", Group: "MiniMax M2 Highspeed", ContextWindow: 200000, SupportsTools: true},
			{ID: "MiniMax-M2.5-highspeed", DisplayName: "MiniMax M2.5 Highspeed", Group: "MiniMax M2 Highspeed", ContextWindow: 200000, SupportsTools: true},
		},
	},
	{
		ID:                 "opencode-cli-glm",
		OpenCodeProviderID: "zhipuai",
		DisplayName:        "GLM (Zhipu AI)",
		Description:        "Zhipu AI's GLM family hosted via the BigModel platform.",
		APIKeyEnvVar:       "ZHIPU_API_KEY",
		APIKeyURL:          "https://open.bigmodel.cn/usercenter/apikeys",
		RequiresAPIKey:     true,
		DefaultModelID:     "glm-4.6",
		TierShortcuts: map[string]string{
			"high":   "glm-5",
			"medium": "glm-4.6",
			"low":    "glm-4.5-flash",
		},
		Models: []OpenCodeSubProviderModel{
			{ID: "glm-5", DisplayName: "GLM-5", Group: "GLM-5", ContextWindow: 200000, SupportsTools: true},
			{ID: "glm-5.1", DisplayName: "GLM-5.1", Group: "GLM-5", ContextWindow: 200000, SupportsTools: true},
			{ID: "glm-4.7", DisplayName: "GLM-4.7", Group: "GLM-4", ContextWindow: 200000, SupportsTools: true},
			{ID: "glm-4.6", DisplayName: "GLM-4.6", Group: "GLM-4", ContextWindow: 200000, SupportsTools: true, IsDefault: true},
			{ID: "glm-4.5", DisplayName: "GLM-4.5", Group: "GLM-4", ContextWindow: 128000, SupportsTools: true},
			{ID: "glm-4.5-flash", DisplayName: "GLM-4.5 Flash", Group: "GLM-4", ContextWindow: 128000, SupportsTools: true},
		},
	},
	{
		// The "coding plan" tile routes through Z.AI's coding-subscription
		// endpoint (api.z.ai/api/coding/paas/v4) rather than the BigModel
		// commerce platform. Mirrors the kimi-for-coding pattern: the model
		// id namespace and the billed entitlement differ from the regular
		// zhipuai tile above. Users on a Z.AI coding subscription should
		// pick this tile; users buying GLM credits on open.bigmodel.cn
		// should pick the opencode-cli-glm tile. Both tiles share the
		// ZHIPU_API_KEY env var — that is the single auth namespace
		// opencode uses for all four GLM/Z.AI provider ids; the tile
		// differs only in endpoint, model namespace, and billing platform.
		ID:                 "opencode-cli-glm-coding-plan",
		OpenCodeProviderID: "zai-coding-plan",
		DisplayName:        "GLM (Z.AI Coding Plan)",
		Description:        "Z.AI's coding-subscription endpoint for GLM. Routes through OpenCode so the assistant identifies as an allowed coding agent.",
		APIKeyEnvVar:       "ZHIPU_API_KEY",
		APIKeyURL:          "https://z.ai/manage-apikey/apikey-list",
		RequiresAPIKey:     true,
		DefaultModelID:     "glm-4.7",
		TierShortcuts: map[string]string{
			"high":   "glm-5-turbo",
			"medium": "glm-4.7",
			"low":    "glm-4.5-air",
		},
		Models: []OpenCodeSubProviderModel{
			{ID: "glm-5-turbo", DisplayName: "GLM-5 Turbo", Group: "GLM-5", ContextWindow: 200000, SupportsTools: true},
			{ID: "glm-4.7", DisplayName: "GLM-4.7", Group: "GLM-4", ContextWindow: 200000, SupportsTools: true, IsDefault: true},
			{ID: "glm-4.5-air", DisplayName: "GLM-4.5 Air", Group: "GLM-4", ContextWindow: 128000, SupportsTools: true},
		},
	},
	{
		ID:                 "opencode-cli-free",
		OpenCodeProviderID: "opencode",
		DisplayName:        "Free Models",
		Description:        "OpenCode-hosted free models. Rate-limited, no API key required.",
		APIKeyEnvVar:       "",
		RequiresAPIKey:     false,
		DefaultModelID:     "deepseek-v4-flash-free",
		TierShortcuts: map[string]string{
			"medium": "deepseek-v4-flash-free",
			"low":    "qwen3.6-plus-free",
		},
		Models: []OpenCodeSubProviderModel{
			{ID: "deepseek-v4-flash-free", DisplayName: "DeepSeek V4 Flash (Free)", Group: "Free", ContextWindow: 128000, SupportsTools: true, IsDefault: true},
			{ID: "qwen3.6-plus-free", DisplayName: "Qwen 3.6 Plus (Free)", Group: "Free", ContextWindow: 131072, SupportsTools: true},
			{ID: "minimax-m2.5-free", DisplayName: "MiniMax M2.5 (Free)", Group: "Free", ContextWindow: 200000, SupportsTools: true},
			{ID: "nemotron-3-super-free", DisplayName: "Nemotron 3 Super (Free)", Group: "Free", ContextWindow: 128000, SupportsTools: true},
			{ID: "big-pickle", DisplayName: "Big Pickle (Free)", Group: "Free", ContextWindow: 128000, SupportsTools: true},
		},
	},
}

// OpenCodeSubProviders returns the full sub-provider catalog. The order is
// the order tiles are rendered in the UI.
func OpenCodeSubProviders() []OpenCodeSubProvider {
	out := make([]OpenCodeSubProvider, len(openCodeSubProviders))
	copy(out, openCodeSubProviders)
	return out
}

// FindOpenCodeSubProvider returns the sub-provider with the given manifest
// ID (e.g. "opencode-cli-kimi"), or false if not registered.
func FindOpenCodeSubProvider(id string) (OpenCodeSubProvider, bool) {
	for _, sp := range openCodeSubProviders {
		if sp.ID == id {
			return sp, true
		}
	}
	return OpenCodeSubProvider{}, false
}

// FindOpenCodeSubProviderByOpenCodeID returns the sub-provider that owns
// the given OpenCode provider namespace (e.g. "kimi-for-coding"), or false
// if not registered.
func FindOpenCodeSubProviderByOpenCodeID(openCodeProviderID string) (OpenCodeSubProvider, bool) {
	for _, sp := range openCodeSubProviders {
		if sp.OpenCodeProviderID == openCodeProviderID {
			return sp, true
		}
	}
	return OpenCodeSubProvider{}, false
}
