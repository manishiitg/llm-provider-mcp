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

var MiniMaxCodingPlanStreamingTestCmd = &cobra.Command{
	Use:   "minimax-coding-plan-streaming",
	Short: "Test MiniMax coding plan streaming with Anthropic model names",
	Run:   runMiniMaxCodingPlanStreamingTest,
}

var mmCodingPlanStreamingModel string

func init() {
	MiniMaxCodingPlanStreamingTestCmd.Flags().StringVar(&mmCodingPlanStreamingModel, "model", "claude-sonnet-4-5", "Anthropic model name to use via MiniMax coding plan")
}

func runMiniMaxCodingPlanStreamingTest(cmd *cobra.Command, args []string) {
	_ = godotenv.Load(".env")
	modelID := mmCodingPlanStreamingModel
	if modelID == "" {
		modelID = "claude-sonnet-4-5"
	}
	log.Printf("Testing MiniMax Coding Plan Streaming with %s", modelID)
	if os.Getenv("MINIMAX_CODING_PLAN_API_KEY") == "" {
		log.Fatal("MINIMAX_CODING_PLAN_API_KEY environment variable is required")
	}
	logger := testing.GetTestLogger()
	llmInstance, err := llmproviders.InitializeLLM(llmproviders.Config{
		Provider:    llmproviders.ProviderMiniMaxCodingPlan,
		ModelID:     modelID,
		Temperature: 0.7,
		Logger:      logger,
	})
	if err != nil {
		log.Fatalf("Failed to create MiniMax Coding Plan LLM: %v", err)
	}
	shared.RunStreamingContentTest(llmInstance, modelID)
}
