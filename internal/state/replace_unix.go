//go:build !windows

package state

import "os"

func replaceFile(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
