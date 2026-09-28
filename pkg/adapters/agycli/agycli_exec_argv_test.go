package agycli

import (
	"slices"
	"testing"
)

func TestAgyExecPinsDefaultModelInAPIKeyMode(t *testing.T) {
	argv := agyBuildExecArgv("hello", DefaultModelID, "", "")
	idx := slices.Index(argv, "--model")
	if idx < 0 || idx+1 >= len(argv) || argv[idx+1] != DefaultModelID {
		t.Fatalf("exec argv did not pin the requested model: %v", argv)
	}
}
