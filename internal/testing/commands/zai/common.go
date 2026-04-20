package zai

import (
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"

	llmproviders "github.com/manishiitg/multi-llm-provider-go"
	"github.com/manishiitg/multi-llm-provider-go/internal/testing"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

const defaultZAIModel = "glm-4.7"

func initZAILogger() {
	logFile := viper.GetString("log-file")
	logLevel := viper.GetString("log-level")
	testing.InitTestLogger(logFile, logLevel)
}

func resolveZAIAPIKey(explicit string) string {
	apiKey := explicit
	if apiKey == "" {
		apiKey = os.Getenv("ZAI_API_KEY")
	}
	if apiKey == "" {
		log.Fatal("API key required: set --api-key flag or ZAI_API_KEY environment variable")
	}
	_ = os.Setenv("ZAI_API_KEY", apiKey)
	return apiKey
}

func createZAITestLLM(modelID string, temperature float64) llmtypes.Model {
	logger := testing.GetTestLogger()
	llmInstance, err := llmproviders.InitializeLLM(llmproviders.Config{
		Provider:    llmproviders.ProviderZAI,
		ModelID:     modelID,
		Temperature: temperature,
		Logger:      logger,
	})
	if err != nil {
		log.Fatalf("Failed to initialize Z.AI LLM: %v", err)
	}
	return llmInstance
}

func loadZAIEnv() {
	_ = godotenv.Load(".env")
}
