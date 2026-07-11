package zai

import (
	"log"

	"github.com/spf13/cobra"

	"github.com/manishiitg/multi-llm-provider-go/internal/testing/commands/shared"
)

var ZAIImageTestCmd = &cobra.Command{
	Use:   "zai-image",
	Short: "Test Z.AI image understanding (vision)",
	Run:   runZAIImageTest,
}

var zaiImageModel string
var zaiImagePath string
var zaiImageURL string

func init() {
	ZAIImageTestCmd.Flags().StringVar(&zaiImageModel, "model", "glm-4.6v", "Z.AI vision-capable model to test")
	ZAIImageTestCmd.Flags().StringVar(&zaiImagePath, "image-path", "", "Path to image file (JPEG, PNG, GIF, WebP)")
	ZAIImageTestCmd.Flags().StringVar(&zaiImageURL, "image-url", "", "URL of image to test")
}

func runZAIImageTest(cmd *cobra.Command, args []string) {
	loadZAIEnv()
	initZAILogger()
	resolveZAIAPIKey("")

	modelID := zaiImageModel
	if modelID == "" {
		modelID = "glm-4.6v"
	}

	log.Printf("🚀 Testing Z.AI Image Understanding with %s", modelID)
	llmInstance := createZAITestLLM(modelID, 1.0)
	shared.RunImageTest(llmInstance, modelID, zaiImagePath, zaiImageURL)
}
