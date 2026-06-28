//go:build cgo && !nocrypto

package bridge

import (
	"errors"

	"github.com/mattn/go-sqlite3"
)

func isSQLiteCorrupt(err error) bool {
	var sqlError sqlite3.Error
	return errors.As(err, &sqlError) && sqlError.Code == sqlite3.ErrCorrupt
}
