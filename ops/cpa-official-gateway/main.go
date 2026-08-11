package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	sdkapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/api"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	cliproxy "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	controlTokenHeader = "X-MadAPI-CPA-Control-Token"
	requestPathHeader  = "X-MadAPI-CPA-Request-Path"
	dispatchIDHeader   = "X-MadAPI-CPA-Dispatch-ID"
	defaultControlURL  = "http://new-api:3000/internal/madapi/cpa"
	defaultPort        = 18317
)

type controlClient struct {
	baseURL string
	token   string
	http    *http.Client
}

type authResponse struct {
	Principal string `json:"principal"`
}

type dispatchResponse struct {
	Ticket string `json:"ticket"`
	Model  string `json:"model"`
	Auth   struct {
		ID string `json:"id"`
	} `json:"auth"`
}

type settleRequest struct {
	Ticket  string       `json:"ticket"`
	Outcome string       `json:"outcome"`
	Usage   usagePayload `json:"usage"`
}

type usagePayload struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

type preparedDispatch struct {
	raw       []byte
	ticket    string
	authID    string
	request   controlDispatchRequest
	prepared  time.Time
	usage     usagePayload
	completed string
}

type controlDispatchRequest struct {
	path    string
	headers http.Header
	body    []byte
}

type requestTicket struct {
	ticket string
	authID string
}

type coordinator struct {
	control *controlClient

	mu        sync.Mutex
	prepared  map[string]*preparedDispatch
	requests  map[string]requestTicket
	finalized map[string]struct{}
}

type newAPIAccessProvider struct {
	control *controlClient
}

func main() {
	controlToken := strings.TrimSpace(os.Getenv("MADAPI_CPA_CONTROL_TOKEN"))
	if len(controlToken) < 32 {
		panic("MADAPI_CPA_CONTROL_TOKEN must contain at least 32 characters")
	}
	controlURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MADAPI_NEWAPI_CONTROL_URL")), "/")
	if controlURL == "" {
		controlURL = defaultControlURL
	}
	client := &controlClient{
		baseURL: controlURL,
		token:   controlToken,
		http: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          256,
			MaxIdleConnsPerHost:   256,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		}},
	}
	coord := &coordinator{
		control:   client,
		prepared:  make(map[string]*preparedDispatch),
		requests:  make(map[string]requestTicket),
		finalized: make(map[string]struct{}),
	}
	if err := runGateway(coord); err != nil {
		panic(err)
	}
}

func runGateway(coord *coordinator) error {
	root, err := os.MkdirTemp("", "madapi-cpa-official-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	authDir := filepath.Join(root, "auths")
	if err = os.MkdirAll(authDir, 0o700); err != nil {
		return err
	}
	port := envPort("MADAPI_CPA_PORT", defaultPort)
	configPath := filepath.Join(root, "config.yaml")
	if err = os.WriteFile(configPath, []byte(bootstrapConfig(port, authDir)), 0o600); err != nil {
		return err
	}
	cfg, err := sdkconfig.LoadConfig(configPath)
	if err != nil {
		return err
	}

	provider := &newAPIAccessProvider{control: coord.control}
	sdkaccess.RegisterProvider("madapi-newapi", provider)
	sdkaccess.SetExclusiveProvider("madapi-newapi")
	accessManager := sdkaccess.NewManager()
	manager := cliproxyauth.NewManager(nil, nil, nil)
	registry := executionregistry.New()

	ready := make(chan struct{})
	service, err := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		WithWatcherFactory(staticWatcherFactory).
		WithCoreAuthManager(manager).
		WithRequestAccessManager(accessManager).
		WithServerOptions(sdkapi.WithRouterConfigurator(func(_ *gin.Engine, base *handlers.BaseAPIHandler, _ *sdkconfig.Config) {
			base.SetPluginHost(coord)
		})).
		WithHooks(cliproxy.Hooks{OnAfterStart: func(service *cliproxy.Service) {
			// The embedded dispatcher uses CPA's official Home scheduler without a
			// standalone Home service. Keep the HTTP server's global Home gate off.
			managerConfig := *cfg
			managerConfig.Home.Enabled = true
			manager.SetConfig(&managerConfig)
			manager.PublishHomeDispatch(coord, registry, 1)
			service.RegisterUsagePlugin(coord)
			close(ready)
		}}).
		Build()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- service.Run(ctx) }()
	select {
	case <-ready:
	case err = <-errCh:
		return err
	case <-time.After(20 * time.Second):
		return errors.New("CPA official gateway startup timed out")
	}
	return <-errCh
}

func staticWatcherFactory(_, _ string, _ func(*sdkconfig.Config)) (*cliproxy.WatcherWrapper, error) {
	return &cliproxy.WatcherWrapper{}, nil
}

func (p *newAPIAccessProvider) Identifier() string { return "madapi-newapi" }

func (p *newAPIAccessProvider) Authenticate(ctx context.Context, request *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if request == nil {
		return nil, sdkaccess.NewNoCredentialsError()
	}
	result, status, err := p.control.authenticate(ctx, request.Header)
	if err == nil && status == http.StatusOK && strings.TrimSpace(result.Principal) != "" {
		return &sdkaccess.Result{Provider: p.Identifier(), Principal: result.Principal, Metadata: map[string]string{"source": "madapi-newapi"}}, nil
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	if err == nil {
		err = fmt.Errorf("NewAPI auth returned status %d", status)
	}
	return nil, sdkaccess.NewInternalAuthError("NewAPI authentication service unavailable", err)
}

func (c *controlClient) authenticate(ctx context.Context, headers http.Header) (authResponse, int, error) {
	var response authResponse
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/auth", nil)
	if err != nil {
		return response, 0, err
	}
	copyClientCredentials(request.Header, headers)
	request.Header.Set(controlTokenHeader, c.token)
	status, body, err := c.do(request)
	if err != nil {
		return response, status, err
	}
	if status == http.StatusOK {
		err = json.Unmarshal(body, &response)
	}
	return response, status, err
}

func (c *controlClient) dispatch(ctx context.Context, input controlDispatchRequest) ([]byte, dispatchResponse, error) {
	var response dispatchResponse
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/dispatch", bytes.NewReader(input.body))
	if err != nil {
		return nil, response, err
	}
	copyClientCredentials(request.Header, input.headers)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(controlTokenHeader, c.token)
	request.Header.Set(requestPathHeader, input.path)
	status, body, err := c.do(request)
	if err != nil {
		return nil, response, err
	}
	if status != http.StatusOK {
		return nil, response, fmt.Errorf("NewAPI dispatch returned %d: %s", status, strings.TrimSpace(string(body)))
	}
	if err = json.Unmarshal(body, &response); err != nil {
		return nil, response, fmt.Errorf("decode NewAPI dispatch: %w", err)
	}
	if response.Ticket == "" || response.Auth.ID == "" {
		return nil, response, errors.New("NewAPI dispatch omitted ticket or auth id")
	}
	return body, response, nil
}

func (c *controlClient) settle(ctx context.Context, requestBody settleRequest) error {
	body, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/settle", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(controlTokenHeader, c.token)
	status, responseBody, err := c.do(request)
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("NewAPI settlement returned %d: %s", status, strings.TrimSpace(string(responseBody)))
}

func (c *controlClient) do(request *http.Request) (int, []byte, error) {
	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	return response.StatusCode, body, err
}

func (c *coordinator) HasRequestInterceptors() bool { return true }
func (c *coordinator) HasStreamInterceptors() bool  { return false }

func (c *coordinator) InterceptRequestBeforeAuth(ctx context.Context, request pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	path := metadataString(request.Metadata, cliproxyexecutor.RequestPathMetadataKey)
	input := controlDispatchRequest{path: path, headers: request.Headers.Clone(), body: bytes.Clone(request.Body)}
	raw, dispatch, err := c.control.dispatch(ctx, input)
	if err != nil {
		return terminateRequest(err, http.StatusServiceUnavailable)
	}
	c.mu.Lock()
	c.prepared[request.RequestID] = &preparedDispatch{
		raw: raw, ticket: dispatch.Ticket, authID: dispatch.Auth.ID,
		request: input, prepared: time.Now(),
	}
	c.requests[request.RequestID] = requestTicket{ticket: dispatch.Ticket, authID: dispatch.Auth.ID}
	c.mu.Unlock()
	if ginContext, ok := ctx.Value("gin").(*gin.Context); ok && ginContext != nil {
		ginContext.Set("userApiKey", dispatch.Ticket)
	}
	headers := make(http.Header)
	headers.Set(dispatchIDHeader, request.RequestID)
	return pluginapi.RequestInterceptResponse{Headers: headers}
}

func (c *coordinator) InterceptRequestAfterAuth(_ context.Context, request pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{ClearHeaders: []string{dispatchIDHeader}}
}

func (c *coordinator) InterceptResponse(_ context.Context, request pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{Body: request.Body}
}

func (c *coordinator) InterceptStreamChunk(_ context.Context, request pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return pluginapi.StreamChunkInterceptResponse{Body: request.Body}
}

func (c *coordinator) CompleteRequest(_ context.Context, completion pluginapi.RequestCompletion) {
	if completion.Outcome != pluginapi.RequestCompletionSucceeded {
		c.finalizeRequest(completion.RequestID, "failed", usagePayload{})
		return
	}
	c.mu.Lock()
	prepared := c.prepared[completion.RequestID]
	if prepared != nil {
		prepared.completed = "succeeded"
	}
	usage := usagePayload{}
	if prepared != nil {
		usage = prepared.usage
	}
	c.mu.Unlock()
	if usage.hasUsage() {
		c.finalizeRequest(completion.RequestID, "succeeded", usage)
		return
	}
	go func(requestID string) {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		<-timer.C
		c.finalizeRequest(requestID, "succeeded", usagePayload{})
	}(completion.RequestID)
}

func (c *coordinator) HeartbeatOK() bool { return true }

func (c *coordinator) RPopAuth(_ context.Context, _ string, _ string, headers http.Header, _ int) ([]byte, error) {
	requestID := strings.TrimSpace(headers.Get(dispatchIDHeader))
	if requestID == "" {
		return nil, errors.New("CPA dispatch id is missing")
	}
	c.mu.Lock()
	prepared := c.prepared[requestID]
	c.mu.Unlock()
	if prepared == nil || len(prepared.raw) == 0 {
		return nil, errors.New("CPA prepared dispatch is unavailable")
	}
	return bytes.Clone(prepared.raw), nil
}

func (*coordinator) AbortAmbiguousDispatch() {}

func (c *coordinator) HandleUsage(_ context.Context, record cliproxyusage.Record) {
	ticket := strings.TrimSpace(record.APIKey)
	if ticket == "" {
		return
	}
	c.mu.Lock()
	if _, done := c.finalized[ticket]; done {
		c.mu.Unlock()
		return
	}
	requestID := ""
	for candidateID, item := range c.requests {
		if item.ticket == ticket {
			prepared := c.prepared[candidateID]
			if prepared != nil && !record.Failed {
				prepared.usage.add(usagePayloadFrom(record.Detail))
			}
			requestID = candidateID
			break
		}
	}
	completed := ""
	usage := usagePayload{}
	if prepared := c.prepared[requestID]; prepared != nil {
		completed = prepared.completed
		usage = prepared.usage
	}
	c.mu.Unlock()
	if requestID != "" && completed == "succeeded" && usage.hasUsage() {
		c.finalizeRequest(requestID, "succeeded", usage)
	}
}

func usagePayloadFrom(detail cliproxyusage.Detail) usagePayload {
	return usagePayload{
		InputTokens: detail.InputTokens, OutputTokens: detail.OutputTokens,
		ReasoningTokens: detail.ReasoningTokens, CachedTokens: detail.CachedTokens,
		CacheReadTokens: detail.CacheReadTokens, CacheCreationTokens: detail.CacheCreationTokens,
		TotalTokens: detail.TotalTokens,
	}
}

func (u *usagePayload) add(other usagePayload) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.ReasoningTokens += other.ReasoningTokens
	u.CachedTokens += other.CachedTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CacheCreationTokens += other.CacheCreationTokens
	u.TotalTokens += other.TotalTokens
}

func (u usagePayload) hasUsage() bool {
	return u.InputTokens != 0 || u.OutputTokens != 0 || u.ReasoningTokens != 0 ||
		u.CachedTokens != 0 || u.CacheReadTokens != 0 || u.CacheCreationTokens != 0 || u.TotalTokens != 0
}

func (c *coordinator) finalizeRequest(requestID, outcome string, usage usagePayload) {
	c.mu.Lock()
	item, exists := c.requests[requestID]
	if !exists {
		c.mu.Unlock()
		return
	}
	delete(c.requests, requestID)
	delete(c.prepared, requestID)
	if _, done := c.finalized[item.ticket]; done {
		c.mu.Unlock()
		return
	}
	c.finalized[item.ticket] = struct{}{}
	c.mu.Unlock()
	_ = c.control.settle(context.Background(), settleRequest{Ticket: item.ticket, Outcome: outcome, Usage: usage})
}

func terminateRequest(err error, status int) pluginapi.RequestInterceptResponse {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"type": "cpa_control_error", "message": err.Error()}})
	return pluginapi.RequestInterceptResponse{Terminate: true, StatusCode: status, ResponseBody: body, ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}}}
}

func copyClientCredentials(dst, src http.Header) {
	for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"} {
		if value := strings.TrimSpace(src.Get(name)); value != "" {
			dst.Set(name, value)
		}
	}
}

func metadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func envPort(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		panic(fmt.Sprintf("invalid %s %q", name, raw))
	}
	return port
}

func bootstrapConfig(port int, authDir string) string {
	return fmt.Sprintf(`host: "0.0.0.0"
port: %d
auth-dir: %s
api-keys: []
debug: false
request-log: false
usage-statistics-enabled: true
request-retry: 0
max-retry-credentials: 1
max-retry-interval: 0
disable-cooling: true
disable-image-generation: false
plugins:
  enabled: false
codex-api-key:
  - api-key: "madapi-bootstrap-unused"
    base-url: "http://127.0.0.1:9"
    models:
      - name: "madapi-bootstrap-unused"
claude-api-key:
  - api-key: "madapi-bootstrap-unused"
    base-url: "http://127.0.0.1:9"
    models:
      - name: "madapi-bootstrap-unused"
gemini-api-key:
  - api-key: "madapi-bootstrap-unused"
    base-url: "http://127.0.0.1:9"
    models:
      - name: "madapi-bootstrap-unused"
xai-api-key:
  - api-key: "madapi-bootstrap-unused"
    base-url: "http://127.0.0.1:9"
    models:
      - name: "madapi-bootstrap-unused"
openai-compatibility:
  - name: "openai-compatibility"
    base-url: "http://127.0.0.1:9/v1"
    api-key-entries:
      - api-key: "madapi-bootstrap-unused"
    models:
      - name: "madapi-bootstrap-unused"
        image: true
`, port, strconv.Quote(filepath.ToSlash(authDir)))
}
