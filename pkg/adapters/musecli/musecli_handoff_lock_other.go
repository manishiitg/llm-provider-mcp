//go:build !darwin && !linux

package musecli

import "fmt"

func lockNativeHandoff(_, _ string) (func(), error) {
	return nil, fmt.Errorf("Muse native session moves are available on Mac and Linux")
}
