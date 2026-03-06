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

var MiniMaxStructuredOutputTestCmd = &cobra.Command{
	Use:   "minimax-structured-output",
	Short: "Test MiniMax structured output (JSON mode)",
	Run:   runMiniMaxStructuredOutputTest,
}

var (
	minimaxStructuredModel        string
	minimaxStructuredUseJSONMode  bool
	minimaxStructuredUseSchema    bool
	minimaxStructuredUseToolBased bool
)

func init() {
	MiniMaxStructuredOutputTestCmd.Flags().StringVar(&minimaxStructuredModel, "model", "MiniMax-M2.5", "MiniMax model to test")
	MiniMaxStructuredOutputTestCmd.Flags().BoolVar(&minimaxStructuredUseJSONMode, "json-mode", true, "Use JSON mode")
	MiniMaxStructuredOutputTestCmd.Flags().BoolVar(&minimaxStructuredUseSchema, "json-schema", false, "Use JSON schema")
	MiniMaxStructuredOutputTestCmd.Flags().BoolVar(&minimaxStructuredUseToolBased, "tool-based", false, "Use tool-based extraction")
}

func runMiniMaxStructuredOutputTest(cmd *cobra.Command, args []string) {
	_ = godotenv.Load(".env")
	modelID := minimaxStructuredModel
	if modelID == "" {
		modelID = "MiniMax-M2.5"
	}
	log.Printf("🚀 Testing MiniMax Structured Output with %s", modelID)
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
	shared.RunStructuredOutputTest(llmInstance, modelID,
		minimaxStructuredUseJSONMode,
		minimaxStructuredUseSchema,
		minimaxStructuredUseToolBased,
	)
}
