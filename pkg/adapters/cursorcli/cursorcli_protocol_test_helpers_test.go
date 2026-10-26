package cursorcli

import (
	"crypto/rand"
	"encoding/hex"
)

func cursorRandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func writeCursorDenyBuiltinHooks(dir string, restore bool, full ...bool) (func(), error) {
	return writeCursorDenyBuiltinHooksWithBridge(dir, restore, len(full) > 0 && full[0], "")
}
