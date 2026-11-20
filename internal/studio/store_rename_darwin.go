//go:build darwin

package studio

import "golang.org/x/sys/unix"

// renameNoReplace renames oldpath to newpath only when newpath does not exist.
func renameNoReplace(oldpath, newpath string) error {
	return unix.RenamexNp(oldpath, newpath, unix.RENAME_EXCL)
}
