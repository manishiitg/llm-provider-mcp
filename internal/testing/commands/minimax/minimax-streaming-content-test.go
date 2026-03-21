package minimax

import (
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	llmproviders "github.com/manishiitg/multi-llm-provider-go"
	"github.com/manishiitg/multi-llm-provider-go/internal/testing"
	"github.com/manishiitg/multi-llm-provider-go/internal/testing/commands/shared"
)

var MiniMaxStreamingContentTestCmd = &cobra.Command{
	Use:   "minimax-streaming-content",
	Short: "Test MiniMax streaming content generation",
	Run:   runMiniMaxStreamingContentTest,
}

var minimaxStreamingContentModel string

func init() {
	MiniMaxStreamingContentTestCmd.Flags().StringVar(&minimaxStreamingContentModel, "model", "MiniMax-M2.7", "MiniMax model to test")
}

func runMiniMaxStreamingContentTest(cmd *cobra.Command, args []string) {
	_ = godotenv.Load(".env")
	modelID := minimaxStreamingContentModel
	if modelID == "" {
		modelID = "MiniMax-M2.7"
	}
	log.Printf("🚀 Testing MiniMax Streaming Content with %s", modelID)
	if os.Getenv("MINIMAX_API_KEY") == "" {
		log.Fatal("MINIMAX_API_KEY environment variable is required")
	}
	logger := testing.GetTestLogger()
	llmInstance, err := llmproviders.InitializeLLM(llmproviders.Config{
		Provider:    llmproviders.ProviderMiniMax,
		ModelID:     modelID,
		Temperature: 0.7,
		Logger:      logger,
	})
	if err != nil {
		log.Fatalf("Failed to create MiniMax LLM: %v", err)
	}
	shared.RunStreamingContentTest(llmInstance, modelID)
}
