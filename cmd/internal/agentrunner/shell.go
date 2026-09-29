package agentrunner

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

func hostShell(getenv func(string) string) (string, error) {
	name := strings.TrimSpace(getenv("HARNESS_SHELL"))
	if name == "" {
		if runtime.GOOS == "windows" {
			name = "pwsh"
		} else {
			name = strings.TrimSpace(getenv("SHELL"))
			if name == "" {
				name = "/bin/sh"
			}
		}
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("resolve shell %q (install PowerShell 7 on Windows or set HARNESS_SHELL): %w", name, err)
	}
	return path, nil
}
