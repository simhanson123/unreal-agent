package openaicodex

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

const fakeAppServerEnvironment = "OPENAICODEX_TEST_FAKE_APP_SERVER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeAppServerEnvironment); mode != "" {
		os.Exit(runFakeAppServer(mode))
	}
	os.Exit(m.Run())
}

// runFakeAppServer imitates `codex app-server`: it rewrites CODEX_HOME/auth.json when asked to
// refresh, emits unrelated notifications, and exits when its input closes.
func runFakeAppServer(mode string) int {
	lines := bufio.NewScanner(os.Stdin)
	for lines.Scan() {
		var request struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
			Params struct {
				RefreshToken bool `json:"refreshToken"`
			} `json:"params"`
		}
		if json.Unmarshal(lines.Bytes(), &request) != nil {
			return 2
		}
		switch request.Method {
		case "initialize":
			fmt.Printf(`{"id":%d,"result":{"userAgent":"fake","codexHome":%q}}`+"\n", *request.ID, os.Getenv("CODEX_HOME"))
		case "account/read":
			fmt.Println(`{"method":"account/updated","params":{}}`)
			switch mode {
			case "api-key":
				fmt.Printf(`{"id":%d,"result":{"account":{"type":"apiKey"},"requiresOpenaiAuth":true}}`+"\n", *request.ID)
				continue
			case "error":
				fmt.Printf(`{"id":%d,"error":{"code":-32000,"message":"refresh token revoked"}}`+"\n", *request.ID)
				continue
			}
			if request.Params.RefreshToken {
				token := testToken("account", time.Now().Add(time.Hour).Unix())
				body := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"account_id":"account"}}`, token)
				if os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"), []byte(body), 0o600) != nil {
					return 3
				}
			}
			fmt.Printf(`{"id":%d,"result":{"account":{"type":"chatgpt","email":null,"planType":"plus"},"requiresOpenaiAuth":true}}`+"\n", *request.ID)
		}
	}
	return 0
}

func fakeRefresher(t *testing.T, mode string) func(context.Context) error {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeAppServerEnvironment, mode)
	return func(ctx context.Context) error {
		return refreshThroughAppServer(ctx, executable, filepath.Dir(currentAuthFile))
	}
}

// currentAuthFile is set per test; tests using it do not run in parallel.
var currentAuthFile string

func TestCodexAppServerRefresh(t *testing.T) {
	for _, test := range []struct {
		mode, match string
	}{
		{mode: "refresh"},
		{mode: "api-key", match: "not signed in with ChatGPT"},
		{mode: "error", match: "refresh token revoked"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			currentAuthFile = filepath.Join(t.TempDir(), "auth.json")
			writeTestAuth(t, currentAuthFile, testToken("account", time.Now().Add(-time.Hour).Unix()), "account")
			err := fakeRefresher(t, test.mode)(t.Context())
			if test.match != "" {
				if err == nil || !strings.Contains(err.Error(), test.match) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (Config{AuthFile: currentAuthFile}).credentials(); err != nil {
				t.Fatalf("refreshed credentials = %v", err)
			}
		})
	}
}

func TestCodexCLIRefresherReportsMissingCLI(t *testing.T) {
	err := CodexCLIRefresher(filepath.Join(t.TempDir(), "missing-codex"), t.TempDir())(t.Context())
	if err == nil || !strings.Contains(err.Error(), "OPENAI_CODEX_CLI") {
		t.Fatalf("error = %v", err)
	}
}

func newTokenServer(t *testing.T, reject func(authorization string) bool) (*httptest.Server, *atomic.Int32, chan string) {
	t.Helper()
	var requests atomic.Int32
	seen := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		authorization := r.Header.Get("Authorization")
		seen <- authorization
		if reject(authorization) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"unauthorized","code":"invalid_token"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	t.Cleanup(server.Close)
	return server, &requests, seen
}

func TestClientRefreshesExpiredLoginAtConstruction(t *testing.T) {
	currentAuthFile = filepath.Join(t.TempDir(), "auth.json")
	expired := testToken("account", time.Now().Add(-time.Hour).Unix())
	writeTestAuth(t, currentAuthFile, expired, "account")
	server, requests, seen := newTokenServer(t, func(string) bool { return false })
	client, err := NewClient(Config{AuthFile: currentAuthFile, BaseURL: server.URL, Refresh: fakeRefresher(t, "refresh")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Respond(t.Context(), llm.Request{}, llm.RequestOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got == "Bearer "+expired || requests.Load() != 1 {
		t.Fatal("expired token was sent instead of the refreshed one")
	}
}

func TestClientRetriesOnceAfterRefreshingRejectedLogin(t *testing.T) {
	currentAuthFile = filepath.Join(t.TempDir(), "auth.json")
	writeTestAuth(t, currentAuthFile, "revoked-token", "account")
	server, requests, seen := newTokenServer(t, func(authorization string) bool { return authorization == "Bearer revoked-token" })
	var refreshes atomic.Int32
	refresh := fakeRefresher(t, "refresh")
	client, err := NewClient(Config{AuthFile: currentAuthFile, BaseURL: server.URL, Refresh: func(ctx context.Context) error {
		refreshes.Add(1)
		return refresh(ctx)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Respond(t.Context(), llm.Request{}, llm.RequestOptions{}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || refreshes.Load() != 1 || <-seen != "Bearer revoked-token" || <-seen == "Bearer revoked-token" {
		t.Fatalf("requests = %d, refreshes = %d", requests.Load(), refreshes.Load())
	}
	// The renewed credentials stay installed for later requests.
	if _, err := client.Respond(t.Context(), llm.Request{}, llm.RequestOptions{}); err != nil || refreshes.Load() != 1 {
		t.Fatalf("second request = %v, refreshes = %d", err, refreshes.Load())
	}
}

func TestClientStopsWhenRefreshKeepsRejectedLogin(t *testing.T) {
	currentAuthFile = filepath.Join(t.TempDir(), "auth.json")
	writeTestAuth(t, currentAuthFile, "revoked-token", "account")
	server, requests, _ := newTokenServer(t, func(string) bool { return true })
	client, err := NewClient(Config{AuthFile: currentAuthFile, BaseURL: server.URL, Refresh: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Respond(t.Context(), llm.Request{}, llm.RequestOptions{})
	if err == nil || !strings.Contains(err.Error(), "codex login") || requests.Load() != 1 {
		t.Fatalf("error = %v, requests = %d", err, requests.Load())
	}
}

func TestClientReportsFailedRefresh(t *testing.T) {
	currentAuthFile = filepath.Join(t.TempDir(), "auth.json")
	writeTestAuth(t, currentAuthFile, testToken("account", time.Now().Add(-time.Hour).Unix()), "account")
	_, err := NewClient(Config{AuthFile: currentAuthFile, Refresh: func(context.Context) error { return errors.New("offline") }})
	if err == nil || !strings.Contains(err.Error(), "refresh Codex login: offline") {
		t.Fatalf("error = %v", err)
	}
	_, err = NewClient(Config{AuthFile: currentAuthFile, Refresh: func(context.Context) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "still expired") {
		t.Fatalf("error = %v", err)
	}
}
