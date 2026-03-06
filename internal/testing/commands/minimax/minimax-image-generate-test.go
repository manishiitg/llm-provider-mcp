package minimax

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	llmproviders "github.com/manishiitg/multi-llm-provider-go"
	"github.com/manishiitg/multi-llm-provider-go/internal/testing"
)

var MiniMaxImageGenerateTestCmd = &cobra.Command{
	Use:   "minimax-image-generate",
	Short: "Test MiniMax image generation (and subject-reference editing with --input-image-url)",
	Run:   runMiniMaxImageGenerateTest,
}

var (
	minimaxImageModel        string
	minimaxImagePrompt       string
	minimaxImageAspect       string
	minimaxImageNum          int
	minimaxImageOutputDir    string
	minimaxImageInputImageURL string
)

func init() {
	MiniMaxImageGenerateTestCmd.Flags().StringVar(&minimaxImageModel, "model", "image-01", "MiniMax image model to use")
	MiniMaxImageGenerateTestCmd.Flags().StringVar(&minimaxImagePrompt, "prompt", "", "Text prompt for image generation")
	MiniMaxImageGenerateTestCmd.Flags().StringVar(&minimaxImageAspect, "aspect-ratio", "1:1", "Aspect ratio: 1:1, 16:9, 9:16, 4:3, 3:4")
	MiniMaxImageGenerateTestCmd.Flags().IntVar(&minimaxImageNum, "num-images", 1, "Number of images to generate")
	MiniMaxImageGenerateTestCmd.Flags().StringVar(&minimaxImageOutputDir, "output-dir", ".", "Directory to save generated images")
	MiniMaxImageGenerateTestCmd.Flags().StringVar(&minimaxImageInputImageURL, "input-image-url", "", "URL of reference image for subject-reference editing")
}

func runMiniMaxImageGenerateTest(cmd *cobra.Command, args []string) {
	_ = godotenv.Load(".env")

	if os.Getenv("MINIMAX_API_KEY") == "" {
		log.Fatal("MINIMAX_API_KEY environment variable is required")
	}

	prompt := minimaxImagePrompt
	if prompt == "" {
		prompt = "A photorealistic image of a serene mountain lake at sunset with reflections"
	}

	log.Printf("Testing MiniMax image generation with model: %s", minimaxImageModel)
	log.Printf("Prompt: %s", prompt)

	logger := testing.GetTestLogger()
	imageGen, err := llmproviders.InitializeImageGenerationModel(llmproviders.Config{
		Provider: llmproviders.ProviderMiniMax,
		ModelID:  minimaxImageModel,
		Logger:   logger,
		Context:  context.Background(),
	})
	if err != nil {
		log.Fatalf("Failed to initialize MiniMax image model: %v", err)
	}

	var genOpts []llmproviders.ImageGenerationOption
	genOpts = append(genOpts, llmproviders.WithNumberOfImages(minimaxImageNum))
	genOpts = append(genOpts, llmproviders.WithAspectRatio(minimaxImageAspect))

	if minimaxImageInputImageURL != "" {
		genOpts = append(genOpts, llmproviders.WithInputImageURL(minimaxImageInputImageURL))
		log.Printf("Using subject-reference URL: %s", minimaxImageInputImageURL)
	}

	log.Printf("Calling GenerateImages...")
	resp, err := imageGen.GenerateImages(context.Background(), prompt, genOpts...)
	if err != nil {
		log.Fatalf("GenerateImages failed: %v", err)
	}

	log.Printf("Generated %d image(s)", len(resp.Images))

	for i, img := range resp.Images {
		filename := filepath.Join(minimaxImageOutputDir, fmt.Sprintf("minimax_image_%d.jpeg", i+1))
		if err := os.WriteFile(filename, img.Data, 0600); err != nil {
			log.Printf("Failed to save image %d: %v", i+1, err)
			continue
		}
		log.Printf("Saved image %d (%s, %d bytes) -> %s", i+1, img.MimeType, len(img.Data), filename)
	}

	log.Printf("MiniMax image generation test completed successfully")
}
