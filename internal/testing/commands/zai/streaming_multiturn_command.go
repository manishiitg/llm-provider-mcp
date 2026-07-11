package zai

import (
	"log"

	"github.com/spf13/cobra"

	"github.com/manishiitg/multi-llm-provider-go/internal/testing/commands/shared"
)

var ZAIStreamingMultiTurnTestCmd = &cobra.Command{
	Use:   "zai-streaming-multiturn",
	Short: "Test Z.AI streaming multi-turn conversation",
	Run:   runZAIStreamingMultiTurnTest,
}

var zaiStreamingMultiTurnModel string

func init() {
	ZAIStreamingMultiTurnTestCmd.Flags().StringVar(&zaiStreamingMultiTurnModel, "model", defaultZAIModel, "Z.AI model to test")
}

func runZAIStreamingMultiTurnTest(cmd *cobra.Command, args []string) {
	loadZAIEnv()
	initZAILogger()
	resolveZAIAPIKey("")

	modelID := zaiStreamingMultiTurnModel
	if modelID == "" {
		modelID = defaultZAIModel
	}

	log.Printf("🚀 Testing Z.AI Streaming Multi-Turn with %s", modelID)
	llmInstance := createZAITestLLM(modelID, 1.0)
	shared.RunStreamingMultiTurnTest(llmInstance, modelID)
}
