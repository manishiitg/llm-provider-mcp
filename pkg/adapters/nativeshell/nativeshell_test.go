package nativeshell

import "testing"

func TestEnabledIsOffUnlessSetOn(t *testing.T) {
	for value, want := range map[string]bool{"": false, "off": false, "0": false, "garbage": false, "on": true, "ON": true, " 1 ": true, "true": true} {
		t.Setenv(EnvVar, value)
		if got := Enabled(); got != want {
			t.Errorf("%s=%q: Enabled()=%v, want %v", EnvVar, value, got, want)
		}
	}
}
