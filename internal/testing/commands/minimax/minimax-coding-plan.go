package minimax

import (
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	llmproviders "github.com/manishiitg/multi-llm-provider-go"
	"github.com/manishiitg/multi-llm-provider-go/internal/testing"
	"github.com/manishiitg/multi-llm-provider-go/internal/testing/commands/shared"
)

var MiniMaxCodingPlanCmd = &cobra.Command{
	Use:   "minimax-coding-plan",
	Short: "Test MiniMax coding plan with Anthropic model names",
	Run:   runMiniMaxCodingPlan,
}

type minimaxCodingPlanFlags struct {
	model string
}

var mmCodingPlanFlags minimaxCodingPlanFlags

func init() {
	MiniMaxCodingPlanCmd.Flags().StringVar(&mmCodingPlanFlags.model, "model", "claude-sonnet-4-5", "Anthropic model name to use via MiniMax coding plan")
}

func runMiniMaxCodingPlan(cmd *cobra.Command, args []string) {
	_ = godotenv.Load(".env")

	logFile := viper.GetString("log-file")
	logLevel := viper.GetString("log-level")
	testing.InitTestLogger(logFile, logLevel)
	logger := testing.GetTestLogger()

	if os.Getenv("MINIMAX_CODING_PLAN_API_KEY") == "" {
		log.Fatal("MINIMAX_CODING_PLAN_API_KEY environment variable is required")
	}

	modelID := mmCodingPlanFlags.model
	if modelID == "" {
		modelID = "claude-sonnet-4-5"
	}

	llmInstance, err := llmproviders.InitializeLLM(llmproviders.Config{
		Provider:    llmproviders.ProviderMiniMaxCodingPlan,
		ModelID:     modelID,
		Temperature: 0.7,
		Logger:      logger,
	})
	if err != nil {
		log.Fatalf("Failed to initialize MiniMax Coding Plan LLM: %v", err)
	}

	shared.RunPlainTextTest(llmInstance, modelID)
}
