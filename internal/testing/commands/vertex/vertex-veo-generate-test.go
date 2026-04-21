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

var VertexVeoGenerateTestCmd = &cobra.Command{
	Use:   "vertex-veo-generate",
	Short: "Test Vertex/Gemini Veo video generation",
	Run:   runVertexVeoGenerateTest,
}

type vertexVeoGenerateFlags struct {
	model           string
	prompt          string
	negativePrompt  string
	aspectRatio     string
	resolution      string
	durationSeconds int
	numVideos       int
	outputDir       string
	generateAudio   bool
}

var vertexVeoFlags vertexVeoGenerateFlags

func init() {
	VertexVeoGenerateTestCmd.Flags().StringVar(&vertexVeoFlags.model, "model", "", "Veo model to use (default: veo-3.1-generate-preview for API-key auth, veo-3.1-generate-001 for Vertex AI auth)")
	VertexVeoGenerateTestCmd.Flags().StringVar(&vertexVeoFlags.prompt, "prompt", "", "Text prompt for video generation")
	VertexVeoGenerateTestCmd.Flags().StringVar(&vertexVeoFlags.negativePrompt, "negative-prompt", "", "Negative prompt (what to exclude)")
	VertexVeoGenerateTestCmd.Flags().StringVar(&vertexVeoFlags.aspectRatio, "aspect-ratio", "16:9", "Aspect ratio: 16:9 or 9:16")
	VertexVeoGenerateTestCmd.Flags().StringVar(&vertexVeoFlags.resolution, "resolution", "", "Resolution: 720p or 1080p")
	VertexVeoGenerateTestCmd.Flags().IntVar(&vertexVeoFlags.durationSeconds, "duration-seconds", 0, "Clip length in seconds (model-dependent)")
	VertexVeoGenerateTestCmd.Flags().IntVar(&vertexVeoFlags.numVideos, "num-videos", 1, "Number of videos to generate")
	VertexVeoGenerateTestCmd.Flags().StringVar(&vertexVeoFlags.outputDir, "output-dir", ".", "Directory to save generated videos")
	VertexVeoGenerateTestCmd.Flags().BoolVar(&vertexVeoFlags.generateAudio, "generate-audio", false, "Request audio generation when supported by the model")
}

func runVertexVeoGenerateTest(cmd *cobra.Command, args []string) {
	_ = godotenv.Load(".env")

	modelID := vertexVeoFlags.model
	if modelID == "" {
		modelID = "veo-3.1-generate-preview"
	}

	prompt := vertexVeoFlags.prompt
	if prompt == "" {
		prompt = "A cinematic drone shot sweeping over monsoon clouds rolling across green mountain ridges at sunrise"
	}

	log.Printf("Testing Veo video generation with model: %s", modelID)
	log.Printf("Prompt: %s", prompt)

	if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("VERTEX_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
		log.Printf("GEMINI_API_KEY, VERTEX_API_KEY, or GOOGLE_API_KEY environment variable is required")
		return
	}

	logger := testing.GetTestLogger()
	videoGen, err := llmproviders.InitializeVideoGenerationModel(llmproviders.Config{
		Provider: llmproviders.ProviderVertex,
		ModelID:  modelID,
		Logger:   logger,
		Context:  context.Background(),
	})
	if err != nil {
		log.Printf("Failed to initialize Veo model: %v", err)
		return
	}

	var genOpts []llmproviders.VideoGenerationOption
	genOpts = append(genOpts, llmproviders.WithVideoNumberOfVideos(vertexVeoFlags.numVideos))
	genOpts = append(genOpts, llmproviders.WithVideoAspectRatio(vertexVeoFlags.aspectRatio))
	if vertexVeoFlags.resolution != "" {
		genOpts = append(genOpts, llmproviders.WithVideoResolution(vertexVeoFlags.resolution))
	}
	if vertexVeoFlags.negativePrompt != "" {
		genOpts = append(genOpts, llmproviders.WithVideoNegativePrompt(vertexVeoFlags.negativePrompt))
	}
	if vertexVeoFlags.durationSeconds > 0 {
		genOpts = append(genOpts, llmproviders.WithVideoDurationSeconds(vertexVeoFlags.durationSeconds))
	}
	if vertexVeoFlags.generateAudio {
		genOpts = append(genOpts, llmproviders.WithVideoGenerateAudio(true))
	}

	log.Printf("Calling GenerateVideos...")
	resp, err := videoGen.GenerateVideos(context.Background(), prompt, genOpts...)
	if err != nil {
		log.Printf("GenerateVideos failed: %v", err)
		return
	}

	log.Printf("Generated %d video(s)", len(resp.Videos))

	if err := os.MkdirAll(vertexVeoFlags.outputDir, 0755); err != nil {
		log.Printf("Failed to create output directory %q: %v", vertexVeoFlags.outputDir, err)
		return
	}

	for i, video := range resp.Videos {
		filename := filepath.Join(vertexVeoFlags.outputDir, fmt.Sprintf("generated_video_%d.mp4", i+1))
		if err := os.WriteFile(filename, video.Data, 0600); err != nil {
			log.Printf("Failed to save video %d: %v", i+1, err)
			continue
		}
		log.Printf("Saved video %d (%s, %d bytes) -> %s", i+1, video.MimeType, len(video.Data), filename)
	}

	log.Printf("Veo generation test completed successfully")
}
