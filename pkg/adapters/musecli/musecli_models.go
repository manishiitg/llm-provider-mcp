package musecli

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// DefaultModelID tracks the CLI catalog default. Verified against the
// on-disk provider catalog (model-catalog/…meta…json, schema_version 1)
// on 2026-09-10: muse-spark-1.3-contributor is_default + is_current,
// context 1007997, output 128000 for all rows.
const DefaultModelID = "muse-spark-1.3-contributor"

type knownMuseModel struct {
	id      string
	name    string
	context int
	output  int
}

var knownMuseCLIModels = []knownMuseModel{
	{id: "muse-spark-1.3-contributor", name: "Muse Spark 1.3 Contributor", context: 1007997, output: 128000},
	{id: "muse-spark-1.3", name: "Muse Spark 1.3", context: 1007997, output: 128000},
	{id: "muse-spark-1.2-contributor", name: "Muse Spark 1.2 Contributor", context: 1007997, output: 128000},
	{id: "muse-spark-1.2", name: "Muse Spark 1.2", context: 1007997, output: 128000},
}

// GetMuseModelMetadata returns catalog metadata for known ids. Unknown ids
// pass through with the provider set (the CLI resolves them); context and
// pricing stay zero there, not guesses.
func GetMuseModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		modelID = DefaultModelID
	}
	for _, known := range knownMuseCLIModels {
		if known.id == modelID {
			return &llmtypes.ModelMetadata{
				Provider:           "muse-cli",
				ModelID:            known.id,
				ModelName:          known.name,
				ContextWindow:      known.context,
				ModelSelectionMode: "dynamic",
			}, nil
		}
	}
	return &llmtypes.ModelMetadata{
		Provider:  "muse-cli",
		ModelID:   modelID,
		ModelName: "Muse (" + modelID + ")",
	}, nil
}

// GetAllMuseCLIModels returns the catalog models for listings and the
// tier-defaults published check.
func GetAllMuseCLIModels() []*llmtypes.ModelMetadata {
	models := make([]*llmtypes.ModelMetadata, 0, len(knownMuseCLIModels))
	adapter := &MuseCLIAdapter{}
	for _, known := range knownMuseCLIModels {
		meta, err := adapter.GetModelMetadata(known.id)
		if err != nil || meta == nil {
			continue
		}
		models = append(models, meta)
	}
	return models
}
