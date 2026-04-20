package zai

import (
	"log"

	"github.com/spf13/cobra"

	"github.com/manishiitg/multi-llm-provider-go/internal/testing/commands/shared"
)

var ZAIStreamingMixedTestCmd = &cobra.Command{
	Use:   "zai-streaming-mixed",
	Short: "Test Z.AI streaming mixed (text + tool calls)",
	Run:   runZAIStreamingMixedTest,
}

var zaiStreamingMixedModel string

func init() {
	ZAIStreamingMixedTestCmd.Flags().StringVar(&zaiStreamingMixedModel, "model", defaultZAIModel, "Z.AI model to test")
}

func runZAIStreamingMixedTest(cmd *cobra.Command, args []string) {
	loadZAIEnv()
	initZAILogger()
	resolveZAIAPIKey("")

	modelID := zaiStreamingMixedModel
	if modelID == "" {
		modelID = defaultZAIModel
	}

	log.Printf("🚀 Testing Z.AI Streaming Mixed with %s", modelID)
	llmInstance := createZAITestLLM(modelID, 1.0)
	shared.RunStreamingMixedTest(llmInstance, modelID)
}
