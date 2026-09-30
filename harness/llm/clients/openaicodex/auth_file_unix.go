//go:build !windows

package openaicodex

import (
	"errors"
	"os"
)

func checkPrivateAuthFile(_ *os.File, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("codex auth file must be a regular file with private permissions (chmod 600)")
	}
	return nil
}
