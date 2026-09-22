package cursorcli

import (
	"reflect"
	"testing"
)

func TestCursorSubmitKeysUseDoubleEnter(t *testing.T) {
	want := []string{"C-m", "C-m"}
	if got := cursorSubmitKeys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("cursor submit keys = %v, want %v", got, want)
	}
}
