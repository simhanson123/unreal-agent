//go:build !windows

package localfile

import "os"

func openAppendFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
}

func publishRename(from, to string) error          { return os.Rename(from, to) }
func syncDirectoryHandle(directory *os.File) error { return directory.Sync() }
