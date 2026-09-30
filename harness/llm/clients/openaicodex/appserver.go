package openaicodex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const refreshTimeout = 90 * time.Second

// CodexCLIRefresher returns a Refresh function that asks the official Codex CLI to refresh its
// managed ChatGPT login (`codex app-server`, account/read with refreshToken). Codex keeps
// ownership of the refresh token and rewrites codexHome/auth.json itself; the harness only
// rereads the file afterwards. An empty executable resolves "codex" on PATH when needed.
func CodexCLIRefresher(executable, codexHome string) func(context.Context) error {
	return func(ctx context.Context) error {
		name := executable
		if name == "" {
			name = "codex"
		}
		path, err := exec.LookPath(name)
		if err != nil {
			return fmt.Errorf("find Codex CLI to refresh the ChatGPT login (install Codex or set OPENAI_CODEX_CLI): %w", err)
		}
		ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
		defer cancel()
		return refreshThroughAppServer(ctx, path, codexHome)
	}
}

type appServerMessage struct {
	ID     *int64         `json:"id"`
	Method string         `json:"method"`
	Result jsontext.Value `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func refreshThroughAppServer(ctx context.Context, path, codexHome string) (err error) {
	command := exec.CommandContext(ctx, path, "app-server")
	command.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	command.WaitDelay = 5 * time.Second
	stdin, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("start Codex app-server: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("start Codex app-server: %w", err)
	}
	stderr := &limitedBuffer{limit: 4096}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start Codex app-server: %w", err)
	}
	defer func() {
		// The app-server exits once its input closes.
		_ = stdin.Close()
		if waitErr := command.Wait(); err == nil && waitErr != nil && ctx.Err() == nil {
			err = fmt.Errorf("codex app-server exited: %w", waitErr)
		}
		if err != nil {
			if detail := strings.TrimSpace(stderr.String()); detail != "" {
				err = fmt.Errorf("%w (codex stderr: %s)", err, detail)
			}
		}
	}()

	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64*1024), 4<<20)
	send := func(message string) error {
		_, err := io.WriteString(stdin, message+"\n")
		return err
	}
	await := func(id int64) (jsontext.Value, error) {
		for lines.Scan() {
			var message appServerMessage
			if json.Unmarshal(lines.Bytes(), &message) != nil {
				continue
			}
			// Notifications and server-initiated requests are not needed here.
			if message.Method != "" || message.ID == nil || *message.ID != id {
				continue
			}
			if message.Error != nil {
				return nil, fmt.Errorf("codex app-server: %s", message.Error.Message)
			}
			return message.Result, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("codex app-server did not answer: %w", err)
		}
		if err := lines.Err(); err != nil {
			return nil, fmt.Errorf("read Codex app-server output: %w", err)
		}
		return nil, errors.New("codex app-server closed its output before answering")
	}

	if err := send(`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"unreal-agent","version":"0"}}}`); err != nil {
		return fmt.Errorf("initialize Codex app-server: %w", err)
	}
	if _, err := await(1); err != nil {
		return err
	}
	if err := send(`{"method":"initialized"}`); err != nil {
		return fmt.Errorf("initialize Codex app-server: %w", err)
	}
	if err := send(`{"id":2,"method":"account/read","params":{"refreshToken":true}}`); err != nil {
		return fmt.Errorf("request Codex login refresh: %w", err)
	}
	result, err := await(2)
	if err != nil {
		return err
	}
	var account struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if err := json.Unmarshal(result, &account); err != nil {
		return fmt.Errorf("decode Codex account: %w", err)
	}
	if account.Account == nil || account.Account.Type != "chatgpt" {
		return errors.New("codex is not signed in with ChatGPT; run `codex login`")
	}
	return nil
}

type limitedBuffer struct {
	mu    sync.Mutex
	limit int
	data  bytes.Buffer
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if room := buffer.limit - buffer.data.Len(); room > 0 {
		buffer.data.Write(data[:min(len(data), room)])
	}
	return len(data), nil
}

func (buffer *limitedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.data.String()
}
