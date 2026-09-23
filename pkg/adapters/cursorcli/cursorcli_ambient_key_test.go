package cursorcli

import (
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestCursorAmbientAPIKeyEnv(t *testing.T) {
	if got := cursorAmbientAPIKeyEnv(nil, "k1", nil); len(got) != 1 || got[0] != "CURSOR_API_KEY=k1" {
		t.Fatalf("ambient key not exported: %v", got)
	}
	if got := cursorAmbientAPIKeyEnv([]string{"CURSOR_API_KEY=explicit"}, "k1", nil); got != nil {
		t.Fatalf("explicit key must win: %v", got)
	}
	if got := cursorAmbientAPIKeyEnv(nil, " ", nil); got != nil {
		t.Fatalf("empty ambient key exported: %v", got)
	}
	opts := &llmtypes.CallOptions{}
	llmtypes.WithCodingAgentSecretEnvironment(map[string]string{"OTHER": "v"})(opts)
	if got := cursorAmbientAPIKeyEnv(nil, "k1", opts); got != nil {
		t.Fatalf("declared scope must not receive the ambient key: %v", got)
	}
}
