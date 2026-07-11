package zai

import (
	"context"
	"fmt"

	llmproviders "github.com/manishiitg/multi-llm-provider-go"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"

	"github.com/spf13/cobra"

	"github.com/manishiitg/multi-llm-provider-go/internal/testing"
	sharedutils "github.com/manishiitg/multi-llm-provider-go/internal/testing/commands/shared"
)

var ZAITokenUsageTestCmd = &cobra.Command{
	Use:   "zai-token-usage",
	Short: "Test Z.AI token usage extraction",
	Run:   runZAITokenUsageTest,
}

var zaiTokenTestModel string
var zaiTokenTestPrompt string

func init() {
	ZAITokenUsageTestCmd.Flags().StringVar(&zaiTokenTestModel, "model", defaultZAIModel, "Z.AI model to test")
	ZAITokenUsageTestCmd.Flags().StringVar(&zaiTokenTestPrompt, "prompt", "Hello world", "Test prompt")
}

func runZAITokenUsageTest(cmd *cobra.Command, args []string) {
	loadZAIEnv()
	initZAILogger()
	resolveZAIAPIKey("")

	fmt.Printf("🧪 Testing Z.AI Token Usage Extraction\n")
	fmt.Printf("=====================================\n\n")

	modelID := zaiTokenTestModel
	if modelID == "" {
		modelID = defaultZAIModel
	}

	logger := testing.GetTestLogger()
	llmInstance, err := llmproviders.InitializeLLM(llmproviders.Config{
		Provider:    llmproviders.ProviderZAI,
		ModelID:     modelID,
		Temperature: 1.0,
		Logger:      logger,
	})
	if err != nil {
		fmt.Printf("❌ Failed to create Z.AI LLM: %v\n", err)
		return
	}

	fmt.Printf("\n🧪 TEST: Z.AI (Simple Query) - %s\n", modelID)
	fmt.Printf("=================================\n")
	messages := []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: zaiTokenTestPrompt}},
		},
	}
	sharedutils.TestLLMTokenUsage(context.Background(), llmInstance, messages, zaiTokenTestPrompt)

	fmt.Printf("\n🧪 TEST: Z.AI (Multi-Turn with Cache) - %s\n", modelID)
	fmt.Printf("==========================================\n")
	sharedutils.TestLLMTokenUsageWithCache(context.Background(), llmInstance)

	fmt.Printf("\n✅ Z.AI Token Usage Test Complete!\n")
}
