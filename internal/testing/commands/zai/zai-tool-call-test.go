package zai

import (
	"log"

	"github.com/spf13/cobra"

	"github.com/manishiitg/multi-llm-provider-go/internal/testing/commands/shared"
)

var ZAIToolCallTestCmd = &cobra.Command{
	Use:   "zai-tool-call",
	Short: "Test Z.AI tool calling",
	Run:   runZAIToolCallTest,
}

var zaiToolCallModel string

func init() {
	ZAIToolCallTestCmd.Flags().StringVar(&zaiToolCallModel, "model", defaultZAIModel, "Z.AI model to test")
}

func runZAIToolCallTest(cmd *cobra.Command, args []string) {
	loadZAIEnv()
	initZAILogger()
	resolveZAIAPIKey("")

	modelID := zaiToolCallModel
	if modelID == "" {
		modelID = defaultZAIModel
	}

	log.Printf("🚀 Testing Z.AI Tool Calling with %s", modelID)
	llmInstance := createZAITestLLM(modelID, 1.0)
	shared.RunToolCallTest(llmInstance, modelID)
}
