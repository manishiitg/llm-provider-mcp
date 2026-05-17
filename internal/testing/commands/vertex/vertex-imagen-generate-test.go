package vertex

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
	VertexImagenGenerateTestCmd.Flags().StringVar(&vertexImagenFlags.model, "model", "", "Gemini/Imagen image model to use (default: gemini-3.1-flash-image-preview)")
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
		modelID = "gemini-3.1-flash-image-preview"
	}

	prompt := vertexImagenFlags.prompt
	if prompt == "" {
		prompt = "A photorealistic image of a serene mountain lake at sunset with reflections"
	}

	log.Printf("Testing Vertex AI Imagen generation with model: %s", modelID)
	log.Printf("Prompt: %s", prompt)

	// Validate required env vars
	if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("VERTEX_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
		log.Fatalf("GEMINI_API_KEY, VERTEX_API_KEY, or GOOGLE_API_KEY environment variable is required")
	}

	logger := testing.GetTestLogger()
	imageGen, err := llmproviders.InitializeImageGenerationModel(llmproviders.Config{
		Provider: llmproviders.ProviderVertex,
		ModelID:  modelID,
		Logger:   logger,
		Context:  context.Background(),
	})
	if err != nil {
		log.Fatalf("Failed to initialize image model: %v", err)
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
		log.Fatalf("GenerateImages failed: %v", err)
	}

	log.Printf("Generated %d image(s)", len(resp.Images))
	if len(resp.Images) == 0 {
		log.Fatalf("GenerateImages returned no images")
	}

	if err := os.MkdirAll(vertexImagenFlags.outputDir, 0750); err != nil {
		log.Fatalf("Failed to create output dir: %v", err)
	}

	for i, img := range resp.Images {
		if len(img.Data) == 0 {
			log.Fatalf("Image %d returned empty data", i+1)
		}
		if contentType := http.DetectContentType(img.Data); !strings.HasPrefix(contentType, "image/") {
			log.Fatalf("Image %d detected content type %q, want image/*", i+1, contentType)
		}

		ext := ".png"
		switch img.MimeType {
		case "image/jpeg":
			ext = ".jpg"
		case "image/webp":
			ext = ".webp"
		}

		filename := filepath.Join(vertexImagenFlags.outputDir, fmt.Sprintf("generated_image_%d%s", i+1, ext))
		if err := os.WriteFile(filename, img.Data, 0600); err != nil {
			log.Fatalf("Failed to save image %d: %v", i+1, err)
		}
		log.Printf("Saved image %d (%s, %d bytes) -> %s", i+1, img.MimeType, len(img.Data), filename)
	}

	log.Printf("Imagen generation test completed successfully")
}
