package geminicli

import (
	"fmt"
	"log"
	"strings"

	llmproviders "github.com/manishiitg/multi-llm-provider-go"
	"github.com/manishiitg/multi-llm-provider-go/internal/testing"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var GeminiCLISearchWebTestCmd = &cobra.Command{
	Use:   "gemini-cli-search-web",
	Short: "Test Gemini CLI native Google web search",
	Run:   runGeminiCLISearchWebTest,
}

func init() {
	GeminiCLISearchWebTestCmd.Flags().String("model", "low", "Gemini CLI model to test")
	GeminiCLISearchWebTestCmd.Flags().String("query", "", "Web search query to run")
	_ = GeminiCLISearchWebTestCmd.MarkFlagRequired("query")
}

func runGeminiCLISearchWebTest(cmd *cobra.Command, args []string) {
	_ = godotenv.Load("agent_go/.env")
	_ = godotenv.Load(".env")
	_ = godotenv.Load("../.env")

	logFile := viper.GetString("log-file")
	logLevel := viper.GetString("log-level")
	testing.InitTestLogger(logFile, logLevel)
	logger := testing.GetTestLogger()

	modelID, _ := cmd.Flags().GetString("model")
	if modelID == "" {
		modelID = "low"
	}

	query, _ := cmd.Flags().GetString("query")
	query = strings.TrimSpace(query)
	if query == "" {
		log.Fatal("query is required")
	}

	llmInstance, err := llmproviders.InitializeLLM(llmproviders.Config{
		Provider: llmproviders.ProviderGeminiCLI,
		ModelID:  modelID,
		Logger:   logger,
	})
	if err != nil {
		log.Fatalf("Failed to initialize Gemini CLI LLM: %v", err)
	}

	result, err := llmproviders.SearchWeb(cmd.Context(), llmInstance, query)
	if err != nil {
		log.Fatalf("Gemini CLI web search failed: %v", err)
	}

	fmt.Println(result)
}
