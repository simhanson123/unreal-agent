package localfile

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func openAppendFile(path string) (*os.File, error) {
	// Windows O_APPEND omits FILE_WRITE_DATA and cannot truncate a torn record.
	// The store's single writer seeks to the committed end before each append.
	return os.OpenFile(path, os.O_WRONLY, 0)
}

func publishRename(from, to string) error {
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, target, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func syncDirectoryHandle(directory *os.File) error {
	// Windows does not expose POSIX directory fsync. File contents are flushed
	// before publication, and publication uses MoveFileEx with WRITE_THROUGH.
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("session store sync handle is not a directory")
	}
	return nil
}
