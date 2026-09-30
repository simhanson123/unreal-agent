//go:build !windows

package openaicodex

import (
	"os"
	"testing"
)

func makeAuthFilePublic(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeAuthFilePrivate is a no-op: test files are created with mode 0600.
func makeAuthFilePrivate(*testing.T, string) {}
