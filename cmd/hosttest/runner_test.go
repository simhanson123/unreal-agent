package hosttest

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/cmd/internal/agentrunner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type hostClient struct {
	name, command    string
	called, verified bool
}

func (client *hostClient) Close() error { return nil }

func (client *hostClient) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	if !client.called {
		client.called = true
		found := false
		for _, current := range request.Tools {
			found = found || current.Name == client.name
		}
		if !found {
			return llm.Response{}, fmt.Errorf("native shell %q not advertised", client.name)
		}
		args, _ := json.Marshal(map[string]string{"command": client.command})
		return llm.Response{ID: "host-1", Stop: llm.StopComplete, Output: []llm.Item{{
			Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "host-call", Name: client.name, Arguments: string(args)},
		}}}, nil
	}
	for _, item := range request.Input {
		if item.Type != llm.ItemToolResult {
			continue
		}
		result := item.Data.(llm.ToolResult)
		if result.CallID == "host-call" && len(result.Output) == 1 && result.Output[0].Value == "native-output" {
			client.verified = true
		}
	}
	return llm.Response{ID: "host-2", Stop: llm.StopComplete,
		Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "done"}}}}, nil
}

func parse(input io.Reader) (agentrunner.Request, agentrunner.ToolFactory, error) {
	var request agentrunner.Request
	err := agentrunner.DecodeRequest(input, &request)
	return request, func(_ context.Context, config agentrunner.ToolConfig) (agentrunner.Tools, error) {
		return agentrunner.Tools{Registry: tool.NewRegistry(config.Translators, request.EnabledTools(config.Names...)...)}, nil
	}, err
}

func TestHostRunnerExecutesNativeShell(t *testing.T) {
	client := &hostClient{name: "Bash", command: "printf native-output"}
	if runtime.GOOS == "windows" {
		client.name, client.command = "Shell", "[Console]::Out.Write('native-output')"
	}
	config := agentrunner.Config{Name: "host-test", ParseRequest: parse, Providers: []agentrunner.Provider{{
		Name: "openai", DefaultModel: "fake-local-model",
		NewClient: func(string, string, int, func(string) string) (agentrunner.Client, error) {
			return client, nil
		},
	}}}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var output, stderr bytes.Buffer
	code := agentrunner.RunMain(ctx, []string{"-workspace", t.TempDir(), "-session-directory", t.TempDir(), "-p", "run native shell"},
		func(string) string { return "" }, func() []string { return nil }, strings.NewReader(""), &output, &stderr, config)
	if code != 0 || !client.verified {
		t.Fatalf("runner exit=%d, verified=%t\nstderr=%s\noutput=%s", code, client.verified, stderr.String(), output.String())
	}
}

func TestHostShellAliasesCannotBypassExclusion(t *testing.T) {
	for _, excluded := range []string{"Bash", "Shell"} {
		request := agentrunner.Request{DisallowedTools: []string{excluded}}
		got := request.EnabledTools("Bash", "Shell", "ViewImage")
		if len(got) != 1 || got[0] != "ViewImage" {
			t.Fatalf("excluded %s but enabled %v", excluded, got)
		}
	}
}
