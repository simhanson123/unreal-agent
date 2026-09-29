// Package hosttest exercises the same public runtime contract on every host.
package hosttest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/primitives"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
	"github.com/unreallabsai/unreal-agent/harness/tool/bash"
)

func helperRequest(t *testing.T, mode string) primitives.ProcessStartRequest {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return primitives.ProcessStartRequest{
		Source: "host-contract", CorrelationID: "start", Path: executable,
		Arguments: []string{"-test.run=^TestHostHelper$", "--", mode},
	}
}

func TestHostHelper(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--" {
			index = i
			break
		}
	}
	if index == -1 {
		return
	}
	switch os.Args[index+1] {
	case "streams":
		fmt.Fprint(os.Stdout, "hello \uD55C\uAE00")
		fmt.Fprint(os.Stderr, "error")
		os.Exit(7)
	case "input":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "large":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 256*1024))
	case "environment":
		cwd, _ := os.Getwd()
		fmt.Fprintf(os.Stdout, "%s\n%s", cwd, os.Getenv("HOST_CONTRACT_VALUE"))
	case "tree", "tree-exit":
		cmd := exec.Command(os.Args[0], "-test.run=^TestHostHelper$", "--", "heartbeat")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		// Wait until the descendant has actually executed before reporting ready.
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(os.Getenv("HOST_HEARTBEAT")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(3)
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Fprint(os.Stdout, "ready")
		if os.Args[index+1] == "tree" {
			time.Sleep(time.Minute)
		}
	case "heartbeat":
		for n := 0; n < 3000; n++ {
			if err := os.WriteFile(os.Getenv("HOST_HEARTBEAT"), []byte(fmt.Sprint(n)), 0o600); err != nil {
				os.Exit(4)
			}
			time.Sleep(20 * time.Millisecond)
		}
	case "owner":
		request := helperRequest(t, "tree")
		request.Pipes = primitives.ProcessPipeStdout
		events := make(chan primitives.PrimitiveEvent, 16)
		primitives.StartProcess(context.Background(), request, events)
		for {
			event := receive(t, events)
			if event.Type == primitives.PrimitiveEventProcessOutput {
				// Deliberately bypass defers to model host death.
				os.Exit(0)
			}
			if event.Type != primitives.PrimitiveEventProcessStarted {
				os.Exit(5)
			}
		}
	default:
		os.Exit(9)
	}
	os.Exit(0)
}

func receive(t *testing.T, events <-chan primitives.PrimitiveEvent) primitives.PrimitiveEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(15 * time.Second):
		t.Fatal("runtime event timed out")
		return primitives.PrimitiveEvent{}
	}
}

func terminal(t *testing.T, events <-chan primitives.PrimitiveEvent) (primitives.PrimitiveEvent, string, string) {
	t.Helper()
	var out, stderr strings.Builder
	for {
		event := receive(t, events)
		if event.Type == primitives.PrimitiveEventProcessOutput {
			chunk := event.Result.(primitives.ProcessOutputResult)
			target := &out
			if chunk.Stream == primitives.ProcessStderr {
				target = &stderr
			}
			if chunk.Offset != int64(target.Len()) {
				t.Fatalf("noncontiguous output offset: %#v", chunk)
			}
			target.Write(chunk.Data)
		}
		switch event.Type {
		case primitives.PrimitiveEventFailed, primitives.PrimitiveEventCanceled, primitives.PrimitiveEventProcessExited:
			return event, out.String(), stderr.String()
		case primitives.PrimitiveEventProcessStreamFailed:
			t.Fatalf("output failure: %#v", event)
		}
	}
}

func TestHostProcessStreamsAndDrains(t *testing.T) {
	for _, mode := range []string{"streams", "large", "input", "environment"} {
		t.Run(mode, func(t *testing.T) {
			request := helperRequest(t, mode)
			request.Pipes = primitives.ProcessPipeAll
			request.Directory = t.TempDir()
			request.Environment = append(os.Environ(), "HOST_CONTRACT_VALUE=value")
			events := make(chan primitives.PrimitiveEvent, 64)
			process := primitives.StartProcess(t.Context(), request, events)
			want := "hello \uD55C\uAE00"
			if mode == "input" {
				control := make(chan primitives.PrimitiveEvent, 2)
				process.WriteInput(t.Context(), primitives.ProcessWriteRequest{Data: []byte(want)}, control)
				if got := receive(t, control); got.Type != primitives.PrimitiveEventProcessInputWritten {
					t.Fatalf("input: %#v", got)
				}
				process.CloseInput(t.Context(), primitives.ProcessCloseInputRequest{}, control)
				if got := receive(t, control); got.Type != primitives.PrimitiveEventProcessInputClosed {
					t.Fatalf("close input: %#v", got)
				}
			}
			event, out, stderr := terminal(t, events)
			if event.Type != primitives.PrimitiveEventProcessExited {
				t.Fatalf("exit: %#v", event)
			}
			exitCode := 0
			switch mode {
			case "streams":
				exitCode = 7
				if stderr != "error" {
					t.Fatalf("stderr = %q", stderr)
				}
			case "large":
				want = strings.Repeat("x", 256*1024)
			case "environment":
				want = request.Directory + "\nvalue"
			}
			if out != want || event.Result.(primitives.ProcessExitResult).ExitCode != exitCode {
				t.Fatalf("output length = %d, expected %d; exit = %#v", len(out), len(want), event.Result)
			}
		})
	}
}

func TestHostProcessTreeCleanup(t *testing.T) {
	for _, mode := range []string{"tree", "tree-exit"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "heartbeat")
			request := helperRequest(t, mode)
			request.Environment = append(os.Environ(), "HOST_HEARTBEAT="+path)
			request.Pipes = primitives.ProcessPipeStdout
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			events := make(chan primitives.PrimitiveEvent, 64)
			primitives.StartProcess(ctx, request, events)
			for {
				event := receive(t, events)
				if event.Type == primitives.PrimitiveEventProcessOutput {
					break
				}
				if event.Type != primitives.PrimitiveEventProcessStarted {
					t.Fatalf("tree startup: %#v", event)
				}
			}
			want := primitives.PrimitiveEventProcessExited
			if mode == "tree" {
				cancel()
				want = primitives.PrimitiveEventCanceled
			}
			event, _, _ := terminal(t, events)
			if event.Type != want {
				t.Fatalf("tree completion: %#v", event)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(150 * time.Millisecond)
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("descendant survived completion: %q -> %q, %v", before, after, err)
			}
		})
	}
}

func TestHostFileCreationAndCapture(t *testing.T) {
	base := filepath.Join(t.TempDir(), "space \uD55C\uAE00")
	events := make(chan primitives.PrimitiveEvent, 64)
	primitives.Create(t.Context(), primitives.IOCreateRequest{Kind: primitives.IOCreateDirectory, Path: base, Mode: 0o700}, events)
	if event := receive(t, events); event.Type != primitives.PrimitiveEventIOCreateCompleted {
		t.Fatalf("mkdir: %#v", event)
	}
	out, stderr := filepath.Join(base, "out"), filepath.Join(base, "err")
	for _, path := range []string{out, stderr} {
		primitives.Create(t.Context(), primitives.IOCreateRequest{Kind: primitives.IOCreateRegularFile, Path: path, Mode: 0o600}, events)
		if event := receive(t, events); event.Type != primitives.PrimitiveEventIOCreateCompleted {
			t.Fatalf("create: %#v", event)
		}
	}
	request := helperRequest(t, "streams")
	request.StdoutPath, request.StderrPath = out, stderr
	primitives.StartProcess(t.Context(), request, events)
	event, _, _ := terminal(t, events)
	if event.Type != primitives.PrimitiveEventProcessExited {
		t.Fatalf("capture: %#v", event)
	}
	content, err := os.ReadFile(out)
	if err != nil || string(content) != "hello \uD55C\uAE00" {
		t.Fatalf("capture content = %q, %v", content, err)
	}
	primitives.Create(t.Context(), primitives.IOCreateRequest{Kind: primitives.IOCreateRegularFile, Path: out, Mode: 0o600}, events)
	if event := receive(t, events); event.Type != primitives.PrimitiveEventIOCreateCompleted {
		t.Fatalf("replay: %#v", event)
	}
	after, _ := os.ReadFile(out)
	if !bytes.Equal(content, after) {
		t.Fatal("replayed create changed existing contents")
	}
}

func TestHostShellOperation(t *testing.T) {
	shell, command := "/bin/sh", "printf 'hello'; printf error >&2; exit 7"
	if runtime.GOOS == "windows" {
		var err error
		shell, err = exec.LookPath("pwsh")
		if err != nil {
			t.Fatal("PowerShell 7 is required:", err)
		}
		command = "[Console]::Out.Write('hello'); [Console]::Error.Write('error'); exit 7"
	}
	spec, err := operation.NewShellSpec(operation.ShellInput{Shell: shell, Command: command, Directory: t.TempDir()}, t.TempDir(), 128)
	if err != nil {
		t.Fatal(err)
	}
	manager := operation.NewLocalOperationManager(t.Context())
	current := operation.Operation{ID: "host-shell", Type: spec.Type, Version: spec.Version,
		State: spec.State, MaxOutputLength: spec.MaxOutputLength, Status: operation.StatusReady}
	if err := manager.Add(current); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case update := <-manager.Updates():
			switch update.Status {
			case operation.StatusCompleted:
				state, err := operation.DecodeShellState(update)
				if err != nil || state.Result == nil || state.Result.Out != "hello" || state.Result.Err != "error" || state.Result.ExitCode != 7 {
					t.Fatalf("shell result = %#v, %v", state, err)
				}
				return
			case operation.StatusFailed, operation.StatusCanceled:
				t.Fatalf("shell failed: %s", update.State)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("shell operation timeout")
		}
	}
}

func TestHostSessionResume(t *testing.T) {
	path := t.TempDir()
	store, err := localfile.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(t.Context(), "host-session"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendInput(t.Context(), "host-session", inbox.Input{
		ID: "request-1", Kind: inbox.InputExternal, Payload: []byte(`"resume me"`),
	}); err != nil {
		t.Fatal(err)
	}
	// Reopening uses only serialized state, not in-memory runtime handles.
	reopened, err := localfile.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Resume(t.Context(), "host-session"); err != nil {
		t.Fatal(err)
	}
	page, err := reopened.Items(t.Context(), "host-session", 0, 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("persisted history = %#v, %v", page, err)
	}
	logPath := filepath.Join(path, "host-session.session.jsonl")
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, append(log, []byte(`{"incomplete":`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reopened.AppendInput(t.Context(), "host-session", inbox.Input{
		ID: "request-2", Kind: inbox.InputExternal, Payload: []byte(`"after recovery"`),
	}); err != nil {
		t.Fatal(err)
	}
	page, err = reopened.Items(t.Context(), "host-session", 0, 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("recovered history = %#v, %v", page, err)
	}
}

func TestHostShellRegistry(t *testing.T) {
	registry := tool.NewRegistry(tool.StaticTranslators{Shell: bash.New(bash.Config{})}, tool.ShellName)
	if definitions := registry.StaticDefinitions(); len(definitions) != 1 || definitions[0].Tool.Name != "Shell" {
		t.Fatalf("definitions: %#v", definitions)
	}
	translator, ok := registry.Resolve("Shell")
	if !ok || translator == nil {
		t.Fatal("Shell translator unavailable")
	}
}
