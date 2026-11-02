//go:build windows

package slotfs

import "os"

// Slots are separate Linux accounts; Windows reports no Unix owner or group,
// so the slot ownership checks do not apply there.
func fileUID(os.FileInfo) (int, bool) { return 0, false }

func fileGID(os.FileInfo) (int, bool) { return 0, false }
