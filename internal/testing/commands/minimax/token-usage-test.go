package minimax

import (
	"context"
	"fmt"
	"os"

	llmproviders "github.com/manishiitg/multi-llm-provider-go"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/manishiitg/multi-llm-provider-go/internal/testing"
	sharedutils "github.com/manishiitg/multi-llm-provider-go/internal/testing/commands/shared"
)

var MiniMaxTokenUsageTestCmd = &cobra.Command{
	Use:   "minimax-token-usage",
	Short: "Test MiniMax token usage extraction",
	Run:   runMiniMaxTokenUsageTest,
}

var minimaxTokenTestModel string
var minimaxTokenTestPrompt string

func init() {
	MiniMaxTokenUsageTestCmd.Flags().StringVar(&minimaxTokenTestModel, "model", "MiniMax-M2.5", "MiniMax model to test")
	MiniMaxTokenUsageTestCmd.Flags().StringVar(&minimaxTokenTestPrompt, "prompt", "Hello world", "Test prompt")
}

func runMiniMaxTokenUsageTest(cmd *cobra.Command, args []string) {
	_ = godotenv.Load(".env")

	fmt.Printf("🧪 Testing MiniMax Token Usage Extraction\n")
	fmt.Printf("==========================================\n\n")

	if os.Getenv("MINIMAX_API_KEY") == "" {
		fmt.Printf("❌ MINIMAX_API_KEY environment variable is required\n")
		return
	}

	modelID := minimaxTokenTestModel
	if modelID == "" {
		modelID = "MiniMax-M2.5"
	}

	logger := testing.GetTestLogger()
	llmInstance, err := llmproviders.InitializeLLM(llmproviders.Config{
		Provider:    llmproviders.ProviderMiniMax,
		ModelID:     modelID,
		Temperature: 0.7,
		Logger:      logger,
	})
	if err != nil {
		fmt.Printf("❌ Failed to create MiniMax LLM: %v\n", err)
		return
	}

	// Test 1: simple prompt
	fmt.Printf("\n🧪 TEST: MiniMax (Simple Query) - %s\n", modelID)
	fmt.Printf("======================================\n")
	messages := []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: minimaxTokenTestPrompt}},
		},
	}
	sharedutils.TestLLMTokenUsage(context.Background(), llmInstance, messages, minimaxTokenTestPrompt)

	// Test 2: multi-turn conversation (exercises cached token path)
	fmt.Printf("\n🧪 TEST: MiniMax (Multi-Turn with Cache) - %s\n", modelID)
	fmt.Printf("=================================================\n")
	sharedutils.TestLLMTokenUsageWithCache(context.Background(), llmInstance)

	fmt.Printf("\n✅ MiniMax Token Usage Test Complete!\n")
}
