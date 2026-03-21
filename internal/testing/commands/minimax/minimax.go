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

var MiniMaxCmd = &cobra.Command{
	Use:   "minimax",
	Short: "Test MiniMax plain text generation",
	Run:   runMiniMax,
}

type minimaxTestFlags struct {
	model  string
	apiKey string
}

var minimaxFlags minimaxTestFlags

func init() {
	MiniMaxCmd.Flags().StringVar(&minimaxFlags.model, "model", "MiniMax-M2.7", "MiniMax model to test")
	MiniMaxCmd.Flags().StringVar(&minimaxFlags.apiKey, "api-key", "", "MiniMax API key (or set MINIMAX_API_KEY env var)")
}

func runMiniMax(cmd *cobra.Command, args []string) {
	_ = godotenv.Load(".env")

	logFile := viper.GetString("log-file")
	logLevel := viper.GetString("log-level")
	testing.InitTestLogger(logFile, logLevel)
	logger := testing.GetTestLogger()

	apiKey := minimaxFlags.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("MINIMAX_API_KEY")
	}
	if apiKey == "" {
		log.Fatal("API key required: set --api-key flag or MINIMAX_API_KEY environment variable")
	}
	os.Setenv("MINIMAX_API_KEY", apiKey)

	modelID := minimaxFlags.model
	if modelID == "" {
		modelID = "MiniMax-M2.7"
	}

	llmInstance, err := llmproviders.InitializeLLM(llmproviders.Config{
		Provider:    llmproviders.ProviderMiniMax,
		ModelID:     modelID,
		Temperature: 0.7,
		Logger:      logger,
	})
	if err != nil {
		log.Fatalf("Failed to initialize MiniMax LLM: %v", err)
	}

	shared.RunPlainTextTest(llmInstance, modelID)
}
