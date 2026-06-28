//go:build !cgo || nocrypto

package bridge

func isSQLiteCorrupt(err error) bool {
	return false
}
