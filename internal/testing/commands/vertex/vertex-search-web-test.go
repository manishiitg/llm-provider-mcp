package vertex

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

var VertexSearchWebTestCmd = &cobra.Command{
	Use:   "vertex-search-web",
	Short: "Test Vertex/Gemini native Google Search via the GenAI SDK",
	Run:   runVertexSearchWebTest,
}

func init() {
	VertexSearchWebTestCmd.Flags().String("model", "gemini-3.6-flash", "Gemini model to test")
	VertexSearchWebTestCmd.Flags().String("query", "", "Web search query to run")
	VertexSearchWebTestCmd.Flags().String("api-key", "", "Google API key (or set VERTEX_API_KEY / GOOGLE_API_KEY env var)")
	_ = VertexSearchWebTestCmd.MarkFlagRequired("query")
}

func runVertexSearchWebTest(cmd *cobra.Command, args []string) {
	_ = godotenv.Load("agent_go/.env")
	_ = godotenv.Load(".env")
	_ = godotenv.Load("../.env")

	logFile := viper.GetString("log-file")
	logLevel := viper.GetString("log-level")
	testing.InitTestLogger(logFile, logLevel)
	logger := testing.GetTestLogger()

	apiKey, _ := cmd.Flags().GetString("api-key")
	if strings.TrimSpace(apiKey) == "" {
		if key := os.Getenv("VERTEX_API_KEY"); strings.TrimSpace(key) != "" {
			apiKey = key
		} else if key := os.Getenv("GOOGLE_API_KEY"); strings.TrimSpace(key) != "" {
			apiKey = key
		}
	}
	if strings.TrimSpace(apiKey) == "" {
		log.Fatal("Google API key required: set --api-key or VERTEX_API_KEY/GOOGLE_API_KEY")
	}
	_ = os.Setenv("VERTEX_API_KEY", apiKey)

	modelID, _ := cmd.Flags().GetString("model")
	if strings.TrimSpace(modelID) == "" {
		modelID = "gemini-3.6-flash"
	}

	query, _ := cmd.Flags().GetString("query")
	query = strings.TrimSpace(query)
	if query == "" {
		log.Fatal("query is required")
	}

	llmInstance, err := llmproviders.InitializeLLM(llmproviders.Config{
		Provider: llmproviders.ProviderVertex,
		ModelID:  modelID,
		Logger:   logger,
	})
	if err != nil {
		log.Fatalf("Failed to initialize Vertex LLM: %v", err)
	}

	result, err := llmproviders.SearchWeb(cmd.Context(), llmInstance, query)
	if err != nil {
		log.Fatalf("Vertex web search failed: %v", err)
	}

	fmt.Println(result)
}
