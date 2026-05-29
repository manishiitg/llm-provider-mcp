package claudecode

import (
	"os"
	"testing"

	"github.com/joho/godotenv"
)

// TestMain loads the repo-root .env (if present) so tests that need real
// provider credentials — e.g. the Vertex/Gemini final-extraction judge in
// TestClaudeFinalExtractionVertexJudgeE2E — pick up GEMINI_API_KEY /
// VERTEX_API_KEY without the caller exporting them by hand.
//
// godotenv.Load does NOT override variables already set in the process
// environment, so an explicit `GEMINI_API_KEY=... go test` still wins.
func TestMain(m *testing.M) {
	// CWD during `go test` is the package dir (pkg/adapters/claudecode); walk
	// up to the repo-root .env.
	for _, p := range []string{".env", "../../../.env", "../../../../.env"} {
		if err := godotenv.Load(p); err == nil {
			break
		}
	}
	os.Exit(m.Run())
}
