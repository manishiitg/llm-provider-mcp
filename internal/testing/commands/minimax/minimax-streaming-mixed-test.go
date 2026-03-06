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

var MiniMaxStreamingMixedTestCmd = &cobra.Command{
	Use:   "minimax-streaming-mixed",
	Short: "Test MiniMax streaming mixed (text + tool calls)",
	Run:   runMiniMaxStreamingMixedTest,
}

var minimaxStreamingMixedModel string

func init() {
	MiniMaxStreamingMixedTestCmd.Flags().StringVar(&minimaxStreamingMixedModel, "model", "MiniMax-M2.5", "MiniMax model to test")
}

func runMiniMaxStreamingMixedTest(cmd *cobra.Command, args []string) {
	_ = godotenv.Load(".env")
	modelID := minimaxStreamingMixedModel
	if modelID == "" {
		modelID = "MiniMax-M2.5"
	}
	log.Printf("🚀 Testing MiniMax Streaming Mixed with %s", modelID)
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
	shared.RunStreamingMixedTest(llmInstance, modelID)
}
