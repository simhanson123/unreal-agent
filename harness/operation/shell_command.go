package operation

import (
	"encoding/base64"
	"encoding/binary"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

func shellArguments(shell, command string) []string {
	switch strings.ToLower(strings.TrimSuffix(filepath.Base(shell), ".exe")) {
	case "pwsh", "powershell":
		// EncodedCommand preserves Unicode and quoting without an intermediate shell.
		script := "[Console]::InputEncoding = [Console]::OutputEncoding = $OutputEncoding = [System.Text.UTF8Encoding]::new($false); " + command
		units := utf16.Encode([]rune(script))
		data := make([]byte, len(units)*2)
		for index, unit := range units {
			binary.LittleEndian.PutUint16(data[index*2:], unit)
		}
		return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(data)}
	default:
		return []string{"-c", command}
	}
}
