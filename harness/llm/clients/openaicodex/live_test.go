package openaicodex

import (
	"os"
	"testing"
)

// TestLiveCodexCLIRefresh asks the installed Codex CLI to refresh the real ChatGPT login and
// checks that the refreshed auth file passes validation. It never prints credentials.
func TestLiveCodexCLIRefresh(t *testing.T) {
	if os.Getenv("OPENAI_CODEX_TEST_LIVE_REFRESH") != "1" {
		t.Skip("set OPENAI_CODEX_TEST_LIVE_REFRESH=1 to refresh the local Codex ChatGPT login")
	}
	config, err := EnvironmentConfig(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if config.Refresh == nil {
		t.Skip("explicit Codex credentials are configured; nothing to refresh")
	}
	if err := config.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	refreshed, err := config.credentials()
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.expiresWithin(refreshMargin) {
		t.Fatal("refreshed token expires too soon")
	}
}
