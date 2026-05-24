package agycli

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

// GetAllAgyCLIModels returns the frontend-visible Antigravity CLI model
// selector. agy manages concrete model selection inside the signed-in account.
func GetAllAgyCLIModels() []*llmtypes.ModelMetadata {
	adapter := &AgyCLIAdapter{}
	meta, err := adapter.GetModelMetadata("agy-cli")
	if err != nil || meta == nil {
		return nil
	}
	return []*llmtypes.ModelMetadata{meta}
}
