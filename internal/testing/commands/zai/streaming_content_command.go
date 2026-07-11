package zai

import (
	"log"

	"github.com/spf13/cobra"

	"github.com/manishiitg/multi-llm-provider-go/internal/testing/commands/shared"
)

var ZAIStreamingContentTestCmd = &cobra.Command{
	Use:   "zai-streaming-content",
	Short: "Test Z.AI streaming content generation",
	Run:   runZAIStreamingContentTest,
}

var zaiStreamingContentModel string

func init() {
	ZAIStreamingContentTestCmd.Flags().StringVar(&zaiStreamingContentModel, "model", defaultZAIModel, "Z.AI model to test")
}

func runZAIStreamingContentTest(cmd *cobra.Command, args []string) {
	loadZAIEnv()
	initZAILogger()
	resolveZAIAPIKey("")

	modelID := zaiStreamingContentModel
	if modelID == "" {
		modelID = defaultZAIModel
	}

	log.Printf("🚀 Testing Z.AI Streaming Content with %s", modelID)
	llmInstance := createZAITestLLM(modelID, 1.0)
	shared.RunStreamingContentTest(llmInstance, modelID)
}
