//go:build !linux && !darwin

package studio

import (
	"errors"
	"os"
)

// renameNoReplace renames oldpath to newpath only when newpath does not exist.
// Without an atomic primitive the existence check and rename race; restore
// refuses an existing destination up front, so the accepted window is between
// that refusal and this publication on secondary platforms.
func renameNoReplace(oldpath, newpath string) error {
	if _, err := os.Lstat(newpath); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(oldpath, newpath)
}
