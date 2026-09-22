package picli

import (
	"reflect"
	"testing"
)

func TestPiSubmitKeysUseDoubleEnter(t *testing.T) {
	want := []string{"Enter", "Enter"}
	if got := piSubmitKeys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("pi submit keys = %v, want %v", got, want)
	}
}
