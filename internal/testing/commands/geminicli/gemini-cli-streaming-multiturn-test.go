package geminicli

import (
	"log"

	llmproviders "github.com/manishiitg/multi-llm-provider-go"
	"github.com/manishiitg/multi-llm-provider-go/internal/testing"
	"github.com/manishiitg/multi-llm-provider-go/internal/testing/commands/shared"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var GeminiCLIStreamingMultiTurnTestCmd = &cobra.Command{
	Use:   "gemini-cli-streaming-multiturn",
	Short: "Test Gemini CLI streaming multi-turn conversation",
	Run:   runGeminiCLIStreamingMultiTurnTest,
}

func init() {
	GeminiCLIStreamingMultiTurnTestCmd.Flags().String("model", "auto", "Gemini model to test")
}

func runGeminiCLIStreamingMultiTurnTest(cmd *cobra.Command, args []string) {
	_ = godotenv.Load("agent_go/.env")
	_ = godotenv.Load(".env")
	_ = godotenv.Load("../.env")

	logFile := viper.GetString("log-file")
	logLevel := viper.GetString("log-level")
	testing.InitTestLogger(logFile, logLevel)
	logger := testing.GetTestLogger()

	modelID, _ := cmd.Flags().GetString("model")
	if modelID == "" {
		modelID = "auto"
	}

	llmInstance, err := llmproviders.InitializeLLM(llmproviders.Config{
		Provider:    llmproviders.ProviderGeminiCLI,
		ModelID:     modelID,
		Temperature: 0.7,
		Logger:      logger,
	})
	if err != nil {
		log.Fatalf("Failed to initialize Gemini CLI LLM: %v", err)
	}

	shared.RunStreamingMultiTurnTest(llmInstance, modelID)
}
