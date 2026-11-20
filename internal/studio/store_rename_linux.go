//go:build linux

package studio

import "golang.org/x/sys/unix"

// renameNoReplace renames oldpath to newpath only when newpath does not exist.
func renameNoReplace(oldpath, newpath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_NOREPLACE)
}
