//go:build windows

package hosttest

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/primitives"
)

func TestHostWindowsCrashKillsJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heartbeat")
	request := helperRequest(t, "owner")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, request.Path, request.Arguments...)
	command.Env = append(os.Environ(), "HOST_HEARTBEAT="+path)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v, %s", err, out)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("child survived host exit: %q -> %q, %v", before, after, err)
	}
}

func TestHostWindowsRejectsSpecialPaths(t *testing.T) {
	for _, name := range []string{"file:stream", "NUL", "CON.txt", "trailing.", "trailing "} {
		t.Run(name, func(t *testing.T) {
			events := make(chan primitives.PrimitiveEvent, 1)
			primitives.Create(t.Context(), primitives.IOCreateRequest{
				Kind: primitives.IOCreateRegularFile, Path: filepath.Join(t.TempDir(), name), Mode: 0o600,
			}, events)
			if got := receive(t, events); got.Type != primitives.PrimitiveEventFailed {
				t.Fatalf("special path accepted: %#v", got)
			}
		})
	}
}

func TestHostWindowsCaptureDoesNotFollowSymlink(t *testing.T) {
	base := t.TempDir()
	target, link := filepath.Join(base, "target"), filepath.Join(base, "link")
	if err := os.WriteFile(target, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	request := helperRequest(t, "streams")
	request.StdoutPath = link
	events := make(chan primitives.PrimitiveEvent, 64)
	primitives.StartProcess(t.Context(), request, events)
	event, _, _ := terminal(t, events)
	if event.Type != primitives.PrimitiveEventFailed {
		t.Fatalf("symlink capture accepted: %#v", event)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}

func TestHostWindowsLongCapturePath(t *testing.T) {
	base := filepath.Join(t.TempDir(), strings.Repeat("a", 100), strings.Repeat("b", 100), strings.Repeat("c", 60))
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "out")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	request := helperRequest(t, "streams")
	request.StdoutPath = path
	events := make(chan primitives.PrimitiveEvent, 64)
	primitives.StartProcess(t.Context(), request, events)
	event, _, _ := terminal(t, events)
	if event.Type != primitives.PrimitiveEventProcessExited {
		t.Fatalf("long path capture failed: %#v", event)
	}
}
