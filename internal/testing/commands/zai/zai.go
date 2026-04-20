package zai

import (
	"github.com/spf13/cobra"

	"github.com/manishiitg/multi-llm-provider-go/internal/testing/commands/shared"
)

var ZAICmd = &cobra.Command{
	Use:   "zai",
	Short: "Test Z.AI plain text generation",
	Run:   runZAI,
}

type zaiTestFlags struct {
	model  string
	apiKey string
}

var flags zaiTestFlags

func init() {
	ZAICmd.Flags().StringVar(&flags.model, "model", defaultZAIModel, "Z.AI model to test")
	ZAICmd.Flags().StringVar(&flags.apiKey, "api-key", "", "Z.AI API key (or set ZAI_API_KEY env var)")
}

func runZAI(cmd *cobra.Command, args []string) {
	loadZAIEnv()
	initZAILogger()
	resolveZAIAPIKey(flags.apiKey)

	modelID := flags.model
	if modelID == "" {
		modelID = defaultZAIModel
	}

	llmInstance := createZAITestLLM(modelID, 1.0)

	shared.RunPlainTextTest(llmInstance, modelID)
}
