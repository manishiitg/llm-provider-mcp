package vertex

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	llmproviders "github.com/manishiitg/multi-llm-provider-go"

	"github.com/manishiitg/multi-llm-provider-go/internal/testing"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

var VertexImagenGenerateTestCmd = &cobra.Command{
	Use:   "vertex-imagen-generate",
	Short: "Test Vertex AI Imagen image generation",
	Run:   runVertexImagenGenerateTest,
}

type vertexImagenGenerateFlags struct {
	model          string
	prompt         string
	negativePrompt string
	aspectRatio    string
	numImages      int
	outputDir      string
}

var vertexImagenFlags vertexImagenGenerateFlags

func init() {
	VertexImagenGenerateTestCmd.Flags().StringVar(&vertexImagenFlags.model, "model", "", "Imagen model to use (default: imagen-3.0-generate-002)")
	VertexImagenGenerateTestCmd.Flags().StringVar(&vertexImagenFlags.prompt, "prompt", "", "Text prompt for image generation")
	VertexImagenGenerateTestCmd.Flags().StringVar(&vertexImagenFlags.negativePrompt, "negative-prompt", "", "Negative prompt (what to exclude)")
	VertexImagenGenerateTestCmd.Flags().StringVar(&vertexImagenFlags.aspectRatio, "aspect-ratio", "1:1", "Aspect ratio: 1:1, 16:9, 9:16, 4:3, 3:4")
	VertexImagenGenerateTestCmd.Flags().IntVar(&vertexImagenFlags.numImages, "num-images", 1, "Number of images to generate (1-4)")
	VertexImagenGenerateTestCmd.Flags().StringVar(&vertexImagenFlags.outputDir, "output-dir", ".", "Directory to save generated images")
}

func runVertexImagenGenerateTest(cmd *cobra.Command, args []string) {
	_ = godotenv.Load(".env")

	modelID := vertexImagenFlags.model
	if modelID == "" {
		modelID = "imagen-4.0-generate-001"
	}

	prompt := vertexImagenFlags.prompt
	if prompt == "" {
		prompt = "A photorealistic image of a serene mountain lake at sunset with reflections"
	}

	log.Printf("Testing Vertex AI Imagen generation with model: %s", modelID)
	log.Printf("Prompt: %s", prompt)

	// Validate required env vars
	if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("VERTEX_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
		log.Printf("GEMINI_API_KEY environment variable is required")
		return
	}

	logger := testing.GetTestLogger()
	imageGen, err := llmproviders.InitializeImageGenerationModel(llmproviders.Config{
		Provider: llmproviders.ProviderVertex,
		ModelID:  modelID,
		Logger:   logger,
		Context:  context.Background(),
	})
	if err != nil {
		log.Printf("Failed to initialize Imagen model: %v", err)
		return
	}

	var genOpts []llmproviders.ImageGenerationOption
	genOpts = append(genOpts, llmproviders.WithNumberOfImages(vertexImagenFlags.numImages))
	genOpts = append(genOpts, llmproviders.WithAspectRatio(vertexImagenFlags.aspectRatio))
	if vertexImagenFlags.negativePrompt != "" {
		genOpts = append(genOpts, llmproviders.WithNegativePrompt(vertexImagenFlags.negativePrompt))
	}

	log.Printf("Calling GenerateImages...")
	resp, err := imageGen.GenerateImages(context.Background(), prompt, genOpts...)
	if err != nil {
		log.Printf("GenerateImages failed: %v", err)
		return
	}

	log.Printf("Generated %d image(s)", len(resp.Images))

	for i, img := range resp.Images {
		filename := filepath.Join(vertexImagenFlags.outputDir, fmt.Sprintf("generated_image_%d.png", i+1))
		if err := os.WriteFile(filename, img.Data, 0600); err != nil {
			log.Printf("Failed to save image %d: %v", i+1, err)
			continue
		}
		log.Printf("Saved image %d (%s, %d bytes) -> %s", i+1, img.MimeType, len(img.Data), filename)
	}

	log.Printf("Imagen generation test completed successfully")
}
