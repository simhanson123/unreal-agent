package openaicodex

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/llm/responsesapi"
	"github.com/unreallabsai/unreal-agent/harness/primitives"
)

const BaseURL = "https://chatgpt.com/backend-api/codex"

// Refresh ahead of expiry so a token does not lapse during a request.
const refreshMargin = 5 * time.Minute

type Config struct {
	AccessToken string
	AccountID   string
	// Read once at construction unless Refresh is set; exclusive with inline credentials.
	AuthFile string
	// Refresh asks the credential owner to renew AuthFile (see CodexCLIRefresher). When set,
	// the client rereads AuthFile before the token expires and once after a 401 response.
	Refresh     func(context.Context) error
	BaseURL     string
	MaxAttempts *int
	Trace       func(responsesapi.Exchange)
}

type Client struct {
	config  Config
	baseURL string
	remote  *primitives.RemoteClient

	mu          sync.Mutex
	adapter     llm.Adapter
	accessToken string
	expires     int64
}

var _ llm.Adapter = (*Client)(nil)

func NewClient(config Config) (*Client, error) {
	baseURL, err := validateBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if config.MaxAttempts != nil && *config.MaxAttempts <= 0 {
		return nil, errors.New("max attempts must be positive")
	}
	if config.Refresh != nil && config.AuthFile == "" {
		return nil, errors.New("codex Refresh requires AuthFile")
	}
	credentials, err := config.credentials()
	if config.Refresh != nil && (errors.Is(err, errExpiredToken) || err == nil && credentials.expiresWithin(refreshMargin)) {
		credentials, err = config.refreshCredentials(context.Background())
	}
	if err != nil {
		return nil, err
	}
	// Never forward subscription credentials through redirects.
	remote := primitives.NewRemoteClientWithHTTPClient(&http.Client{
		Transport:     http.DefaultTransport.(*http.Transport).Clone(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	})
	client := &Client{config: config, baseURL: baseURL, remote: remote}
	if err := client.install(credentials); err != nil {
		_ = remote.Close()
		return nil, err
	}
	return client, nil
}

func (client *Client) install(credentials credentials) error {
	adapter, err := responsesapi.NewAdapter(client.remote, responsesapi.Config{
		Endpoint: client.baseURL + "/responses",
		Headers: map[string][]string{
			"Authorization":      {"Bearer " + credentials.accessToken},
			"ChatGPT-Account-ID": {credentials.accountID},
			"Content-Type":       {"application/json"},
			"originator":         {"unreal-agent"},
			"User-Agent":         {"unreal-agent"},
		},
		CacheKeyPlacement: responsesapi.CacheKeyPlacement{UsePromptCacheKeyField: true, Header: "session-id"},
		MaxAttempts:       client.config.MaxAttempts,
		Trace:             client.config.Trace,
	})
	if err != nil {
		return err
	}
	client.adapter, client.accessToken, client.expires = adapter, credentials.accessToken, credentials.expires
	return nil
}

func (config Config) refreshCredentials(ctx context.Context) (credentials, error) {
	if err := config.Refresh(ctx); err != nil {
		return credentials{}, fmt.Errorf("refresh Codex login: %w", err)
	}
	refreshed, err := config.credentials()
	if errors.Is(err, errExpiredToken) {
		return credentials{}, errors.New("codex access token is still expired after refreshing through the Codex CLI; run `codex login`")
	}
	return refreshed, err
}

func (credentials credentials) expiresWithin(margin time.Duration) bool {
	return credentials.expires != 0 && time.Now().Add(margin).Unix() >= credentials.expires
}

// current returns the adapter to use, renewing credentials first when they are about to expire.
func (client *Client) current(ctx context.Context) (llm.Adapter, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.config.Refresh != nil && (credentials{expires: client.expires}).expiresWithin(refreshMargin) {
		if err := client.renewLocked(ctx); err != nil {
			return nil, err
		}
	}
	return client.adapter, nil
}

// renewAfterUnauthorized renews credentials once for a request rejected with stale, unless a
// concurrent request already installed newer credentials.
func (client *Client) renewAfterUnauthorized(ctx context.Context, rejected llm.Adapter) (llm.Adapter, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.adapter != rejected {
		return client.adapter, nil
	}
	previous := client.accessToken
	if err := client.renewLocked(ctx); err != nil {
		return nil, err
	}
	if client.accessToken == previous {
		return nil, errors.New("codex login refresh returned the rejected token; run `codex login`")
	}
	return client.adapter, nil
}

func (client *Client) renewLocked(ctx context.Context) error {
	credentials, err := client.config.refreshCredentials(ctx)
	if err != nil {
		return err
	}
	return client.install(credentials)
}

func (client *Client) Respond(ctx context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	if request.Model.MaxOutputTokens != nil {
		return llm.Response{}, errors.New("codex does not support max_output_tokens")
	}
	adapter, err := client.current(ctx)
	if err != nil {
		return llm.Response{}, err
	}
	response, err := adapter.Respond(ctx, request, options)
	if unauthorized(err) && client.config.Refresh != nil {
		// A 401 is returned before the request is processed, so one retry is safe.
		renewed, renewErr := client.renewAfterUnauthorized(ctx, adapter)
		if renewErr != nil {
			return llm.Response{}, fmt.Errorf("codex credentials rejected and could not be renewed: %w", errors.Join(renewErr, err))
		}
		response, err = renewed.Respond(ctx, request, options)
	}
	if unauthorized(err) {
		return llm.Response{}, fmt.Errorf("codex credentials rejected; renew them externally (run `codex login`) and recreate the client: %w", err)
	}
	return response, err
}

func (client *Client) Close() error { return client.remote.Close() }

func unauthorized(err error) bool {
	var apiErr *responsesapi.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized
}

func validateBaseURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" || value == BaseURL {
		return BaseURL, nil
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") {
		ip := net.ParseIP(parsed.Hostname())
		if ip != nil && ip.IsLoopback() {
			return value, nil
		}
	}
	return "", errors.New("codex base URL must be https://chatgpt.com/backend-api/codex or an explicit loopback IP endpoint")
}
