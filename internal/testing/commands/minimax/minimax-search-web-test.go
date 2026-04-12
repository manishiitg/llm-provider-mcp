package minimax

import (
	"fmt"
	"log"
	"os"
	"strings"

	llmproviders "github.com/manishiitg/multi-llm-provider-go"
	"github.com/manishiitg/multi-llm-provider-go/internal/testing"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var MiniMaxSearchWebTestCmd = &cobra.Command{
	Use:   "minimax-search-web",
	Short: "Test MiniMax Coding Plan native web search via mmx CLI",
	Run:   runMiniMaxSearchWebTest,
}

func init() {
	MiniMaxSearchWebTestCmd.Flags().String("model", "claude-sonnet-4-5", "MiniMax coding plan model to associate with the provider")
	MiniMaxSearchWebTestCmd.Flags().String("query", "", "Web search query to run")
	MiniMaxSearchWebTestCmd.Flags().String("api-key", "", "MiniMax coding plan API key (or set MINIMAX_CODING_PLAN_API_KEY env var)")
	_ = MiniMaxSearchWebTestCmd.MarkFlagRequired("query")
}

func runMiniMaxSearchWebTest(cmd *cobra.Command, args []string) {
	_ = godotenv.Load("agent_go/.env")
	_ = godotenv.Load(".env")
	_ = godotenv.Load("../.env")

	logFile := viper.GetString("log-file")
	logLevel := viper.GetString("log-level")
	testing.InitTestLogger(logFile, logLevel)
	logger := testing.GetTestLogger()

	apiKey, _ := cmd.Flags().GetString("api-key")
	if strings.TrimSpace(apiKey) == "" {
		apiKey = os.Getenv("MINIMAX_CODING_PLAN_API_KEY")
	}
	if strings.TrimSpace(apiKey) == "" {
		log.Fatal("MiniMax coding plan API key required: set --api-key or MINIMAX_CODING_PLAN_API_KEY")
	}
	_ = os.Setenv("MINIMAX_CODING_PLAN_API_KEY", apiKey)

	modelID, _ := cmd.Flags().GetString("model")
	if strings.TrimSpace(modelID) == "" {
		modelID = "claude-sonnet-4-5"
	}

	query, _ := cmd.Flags().GetString("query")
	query = strings.TrimSpace(query)
	if query == "" {
		log.Fatal("query is required")
	}

	llmInstance, err := llmproviders.InitializeLLM(llmproviders.Config{
		Provider: llmproviders.ProviderMiniMaxCodingPlan,
		ModelID:  modelID,
		Logger:   logger,
	})
	if err != nil {
		log.Fatalf("Failed to initialize MiniMax coding plan LLM: %v", err)
	}

	result, err := llmproviders.SearchWeb(cmd.Context(), llmInstance, query)
	if err != nil {
		log.Fatalf("MiniMax coding plan web search failed: %v", err)
	}

	fmt.Println(result)
}
