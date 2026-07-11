package zai

import (
	"log"

	"github.com/spf13/cobra"

	"github.com/manishiitg/multi-llm-provider-go/internal/testing/commands/shared"
)

var ZAIStructuredOutputTestCmd = &cobra.Command{
	Use:   "zai-structured-output",
	Short: "Test Z.AI structured output",
	Run:   runZAIStructuredOutputTest,
}

var (
	zaiStructuredModel        string
	zaiStructuredUseJSONMode  bool
	zaiStructuredUseSchema    bool
	zaiStructuredUseToolBased bool
)

func init() {
	ZAIStructuredOutputTestCmd.Flags().StringVar(&zaiStructuredModel, "model", defaultZAIModel, "Z.AI model to test")
	ZAIStructuredOutputTestCmd.Flags().BoolVar(&zaiStructuredUseJSONMode, "json-mode", true, "Use JSON mode")
	ZAIStructuredOutputTestCmd.Flags().BoolVar(&zaiStructuredUseSchema, "json-schema", false, "Use JSON schema")
	ZAIStructuredOutputTestCmd.Flags().BoolVar(&zaiStructuredUseToolBased, "tool-based", false, "Use tool-based extraction")
}

func runZAIStructuredOutputTest(cmd *cobra.Command, args []string) {
	loadZAIEnv()
	initZAILogger()
	resolveZAIAPIKey("")

	modelID := zaiStructuredModel
	if modelID == "" {
		modelID = defaultZAIModel
	}

	log.Printf("🚀 Testing Z.AI Structured Output with %s", modelID)
	llmInstance := createZAITestLLM(modelID, 1.0)
	shared.RunStructuredOutputTest(llmInstance, modelID, zaiStructuredUseJSONMode, zaiStructuredUseSchema, zaiStructuredUseToolBased)
}
