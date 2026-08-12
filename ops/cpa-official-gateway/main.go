package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/tidwall/gjson"
)

const (
	controlTokenHeader  = "X-MadAPI-CPA-Control-Token"
	requestPathHeader   = "X-MadAPI-CPA-Request-Path"
	dispatchIDHeader    = "X-MadAPI-CPA-Dispatch-ID"
	executeTokenHeader  = "X-MadAPI-CPA-Execute-Token"
	executeIDHeader     = "X-MadAPI-CPA-Execute-ID"
	executeUsageTrailer = "X-MadAPI-CPA-Usage"
	defaultControlURL   = "http://new-api:3000/internal/madapi/cpa"
	defaultPort         = 18317
	defaultExecutePort  = 18417
	executeMetaLimit    = 1 << 20
	executeBodyLimit    = 64 << 20
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
	raw         []byte
	ticket      string
	authID      string
	request     controlDispatchRequest
	prepared    time.Time
	usage       usagePayload
	completed   string
	synchronous bool
	notify      chan struct{}
}

type executeMeta struct {
	Provider      string      `json:"provider"`
	ChannelID     int         `json:"channel_id"`
	UserID        int         `json:"user_id"`
	BaseURL       string      `json:"base_url"`
	APIKey        string      `json:"api_key"`
	Model         string      `json:"model"`
	OriginalModel string      `json:"original_model"`
	Headers       http.Header `json:"headers,omitempty"`
	RequestPath   string      `json:"request_path"`
}

type executeAuthRecord struct {
	ID         string            `json:"id"`
	Provider   string            `json:"provider"`
	Status     string            `json:"status"`
	Attributes map[string]string `json:"attributes"`
	Metadata   map[string]any    `json:"metadata,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type executeDispatchResponse struct {
	Ticket        string            `json:"ticket"`
	Model         string            `json:"model"`
	Provider      string            `json:"provider"`
	AuthIndex     string            `json:"auth_index"`
	UserAPIKey    string            `json:"user_api_key"`
	OriginalAlias string            `json:"original_alias,omitempty"`
	ForceMapping  bool              `json:"force_mapping,omitempty"`
	Auth          executeAuthRecord `json:"auth"`
}

type codexOAuthKey struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
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

	mu           sync.Mutex
	prepared     map[string]*preparedDispatch
	preloaded    map[string]*preparedDispatch
	requests     map[string]requestTicket
	finalized    map[string]struct{}
	executeToken string
}

type newAPIAccessProvider struct {
	control       *controlClient
	internalToken string
}

var executeSequence atomic.Uint64

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
		control:      client,
		prepared:     make(map[string]*preparedDispatch),
		preloaded:    make(map[string]*preparedDispatch),
		requests:     make(map[string]requestTicket),
		finalized:    make(map[string]struct{}),
		executeToken: controlToken,
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

	provider := &newAPIAccessProvider{control: coord.control, internalToken: coord.executeToken}
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
	executeServer := newExecuteServer(coord, port)
	executeErrCh := make(chan error, 1)
	go func() { executeErrCh <- executeServer.ListenAndServe() }()
	select {
	case err = <-errCh:
		return err
	case err = <-executeErrCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func staticWatcherFactory(_, _ string, _ func(*sdkconfig.Config)) (*cliproxy.WatcherWrapper, error) {
	return &cliproxy.WatcherWrapper{}, nil
}

func newExecuteServer(coord *coordinator, cpaPort int) *http.Server {
	port := envPort("MADAPI_CPA_EXECUTE_PORT", defaultExecutePort)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"runtime":"official-cpa-handler-v7.2.128"}`))
	})
	mux.Handle("/execute", executeHandler(coord, cpaPort))
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

func executeHandler(coord *coordinator, cpaPort int) http.Handler {
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !privateRemote(r.RemoteAddr) || !constantTimeToken(coord.executeToken, r.Header.Get(executeTokenHeader)) {
			http.NotFound(w, r)
			return
		}
		meta, payload, err := readExecuteFrame(r.Body)
		if err != nil {
			writeExecuteError(w, http.StatusBadRequest, err)
			return
		}
		prepared, executeID, err := prepareExecuteDispatch(meta)
		if err != nil {
			writeExecuteError(w, http.StatusBadRequest, err)
			return
		}
		coord.mu.Lock()
		coord.preloaded[executeID] = prepared
		coord.mu.Unlock()
		defer func() {
			coord.mu.Lock()
			delete(coord.preloaded, executeID)
			coord.mu.Unlock()
		}()

		requestURL := fmt.Sprintf("http://127.0.0.1:%d%s", cpaPort, meta.RequestPath)
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, requestURL, bytes.NewReader(payload))
		if err != nil {
			writeExecuteError(w, http.StatusInternalServerError, err)
			return
		}
		copyEndToEndHeaders(request.Header, meta.Headers)
		request.Header.Set("Authorization", "Bearer "+coord.executeToken)
		request.Header.Set(executeIDHeader, executeID)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			writeExecuteError(w, http.StatusBadGateway, err)
			return
		}
		defer response.Body.Close()
		copyEndToEndHeaders(w.Header(), response.Header)
		streaming := gjson.GetBytes(payload, "stream").Bool()
		w.Header().Add("Trailer", executeUsageTrailer)
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
		if !streaming {
			usage, usageErr := coord.waitSynchronousUsage(r.Context(), prepared, 15*time.Second)
			if usageErr != nil {
				coord.cleanupSynchronousByPointer(prepared)
				return
			}
			if encoded, encodeErr := json.Marshal(usage); encodeErr == nil {
				w.Header().Set(executeUsageTrailer, hex.EncodeToString(encoded))
			}
		} else if usage := coord.synchronousUsage(prepared); usage.hasUsage() {
			if encoded, encodeErr := json.Marshal(usage); encodeErr == nil {
				w.Header().Set(executeUsageTrailer, hex.EncodeToString(encoded))
			}
		}
		coord.cleanupSynchronousByPointer(prepared)
	})
}

func readExecuteFrame(body io.Reader) (executeMeta, []byte, error) {
	var meta executeMeta
	var prefix [4]byte
	if _, err := io.ReadFull(body, prefix[:]); err != nil {
		return meta, nil, fmt.Errorf("read execute metadata length: %w", err)
	}
	metadataLength := int(binary.BigEndian.Uint32(prefix[:]))
	if metadataLength < 2 || metadataLength > executeMetaLimit {
		return meta, nil, fmt.Errorf("invalid execute metadata length %d", metadataLength)
	}
	metadata := make([]byte, metadataLength)
	if _, err := io.ReadFull(body, metadata); err != nil {
		return meta, nil, fmt.Errorf("read execute metadata: %w", err)
	}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		return meta, nil, fmt.Errorf("decode execute metadata: %w", err)
	}
	payload, err := io.ReadAll(io.LimitReader(body, executeBodyLimit+1))
	if err != nil {
		return meta, nil, err
	}
	if len(payload) == 0 || len(payload) > executeBodyLimit {
		return meta, nil, fmt.Errorf("invalid execute payload length %d", len(payload))
	}
	return meta, payload, nil
}

func prepareExecuteDispatch(meta executeMeta) (*preparedDispatch, string, error) {
	meta.Provider = strings.TrimSpace(meta.Provider)
	meta.Model = strings.TrimSpace(meta.Model)
	meta.BaseURL = strings.TrimRight(strings.TrimSpace(meta.BaseURL), "/")
	meta.APIKey = strings.TrimSpace(meta.APIKey)
	switch meta.RequestPath {
	case "/v1/responses", "/v1/responses/compact", "/v1/images/generations", "/v1/images/edits":
	default:
		return nil, "", fmt.Errorf("unsupported CPA execute path %q", meta.RequestPath)
	}
	if meta.Provider == "" || meta.Model == "" || meta.APIKey == "" {
		return nil, "", errors.New("provider, model and API key are required")
	}
	auth, err := executeAuth(meta)
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	executeID := fmt.Sprintf("sync-%d", executeSequence.Add(1))
	dispatch := executeDispatchResponse{
		Ticket: executeID, Model: meta.Model, Provider: meta.Provider,
		AuthIndex: strconv.Itoa(meta.ChannelID), UserAPIKey: executeID,
		OriginalAlias: meta.OriginalModel, ForceMapping: meta.OriginalModel != "" && meta.OriginalModel != meta.Model,
		Auth: executeAuthRecord{ID: auth.ID, Provider: auth.Provider, Status: string(auth.Status), Attributes: auth.Attributes, Metadata: auth.Metadata, CreatedAt: now, UpdatedAt: now},
	}
	raw, err := json.Marshal(dispatch)
	if err != nil {
		return nil, "", err
	}
	return &preparedDispatch{raw: raw, ticket: executeID, authID: auth.ID, synchronous: true, notify: make(chan struct{}, 1)}, executeID, nil
}

func executeAuth(meta executeMeta) (*cliproxyauth.Auth, error) {
	attributes := map[string]string{"api_key": meta.APIKey, "auth_kind": "api-key", "runtime_only": "true"}
	if meta.BaseURL != "" {
		attributes["base_url"] = meta.BaseURL
	}
	var metadata map[string]any
	if meta.Provider == "xai" {
		attributes["using_api"] = "true"
	}
	if meta.Provider == "codex" && strings.HasPrefix(meta.APIKey, "{") {
		var key codexOAuthKey
		if err := json.Unmarshal([]byte(meta.APIKey), &key); err != nil {
			return nil, errors.New("invalid Codex OAuth channel credential")
		}
		key.AccessToken = strings.TrimSpace(key.AccessToken)
		key.AccountID = strings.TrimSpace(key.AccountID)
		if key.AccessToken == "" || key.AccountID == "" {
			return nil, errors.New("incomplete Codex OAuth channel credential")
		}
		delete(attributes, "api_key")
		attributes["auth_kind"] = "oauth"
		metadata = map[string]any{"access_token": key.AccessToken, "account_id": key.AccountID}
	}
	for name, values := range meta.Headers {
		if len(values) > 0 && strings.TrimSpace(name) != "" {
			attributes["header:"+name] = strings.TrimSpace(values[len(values)-1])
		}
	}
	sum := sha256.Sum256([]byte(meta.Provider + "\x00" + strconv.Itoa(meta.ChannelID) + "\x00" + meta.BaseURL + "\x00" + meta.APIKey))
	return &cliproxyauth.Auth{ID: fmt.Sprintf("madapi-channel-%d-%s-%s", meta.ChannelID, meta.Provider, hex.EncodeToString(sum[:8])), Provider: meta.Provider, Status: cliproxyauth.StatusActive, Attributes: attributes, Metadata: metadata}, nil
}

func bearerToken(header http.Header) string {
	value := strings.TrimSpace(header.Get("Authorization"))
	if len(value) > 7 && strings.EqualFold(value[:7], "Bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func privateRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(remoteAddr), "[]")
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func constantTimeToken(expected, provided string) bool {
	provided = strings.TrimSpace(provided)
	return expected != "" && len(expected) == len(provided) && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

func copyEndToEndHeaders(dst, src http.Header) {
	for name, values := range src {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "authorization", "x-api-key", "x-goog-api-key", "host", "connection", "content-length", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func writeExecuteError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "cpa_handler_error", "message": err.Error()}})
}

func (p *newAPIAccessProvider) Identifier() string { return "madapi-newapi" }

func (p *newAPIAccessProvider) Authenticate(ctx context.Context, request *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if request == nil {
		return nil, sdkaccess.NewNoCredentialsError()
	}
	if privateRemote(request.RemoteAddr) && bearerToken(request.Header) == p.internalToken {
		return &sdkaccess.Result{Provider: p.Identifier(), Principal: "madapi-newapi-internal", Metadata: map[string]string{"source": "madapi-newapi-sync"}}, nil
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
	if executeID := strings.TrimSpace(request.Headers.Get(executeIDHeader)); executeID != "" {
		c.mu.Lock()
		prepared := c.preloaded[executeID]
		delete(c.preloaded, executeID)
		if prepared != nil {
			prepared.request = input
			prepared.prepared = time.Now()
			prepared.synchronous = true
			c.prepared[request.RequestID] = prepared
			c.requests[request.RequestID] = requestTicket{ticket: prepared.ticket, authID: prepared.authID}
		}
		c.mu.Unlock()
		if prepared == nil {
			return terminateRequest(errors.New("CPA synchronous dispatch is unavailable"), http.StatusServiceUnavailable)
		}
		headers := make(http.Header)
		headers.Set(dispatchIDHeader, request.RequestID)
		body, err := rewriteDispatchModel(input.body, preparedModel(prepared.raw))
		if err != nil {
			c.cleanupSynchronous(request.RequestID)
			return terminateRequest(err, http.StatusBadGateway)
		}
		return pluginapi.RequestInterceptResponse{Headers: headers, ClearHeaders: []string{executeIDHeader}, Body: body}
	}
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
	body, err := rewriteDispatchModel(input.body, dispatch.Model)
	if err != nil {
		c.finalizeRequest(request.RequestID, "failed", usagePayload{})
		return terminateRequest(err, http.StatusBadGateway)
	}
	return pluginapi.RequestInterceptResponse{Headers: headers, Body: body}
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
	c.mu.Lock()
	prepared := c.prepared[completion.RequestID]
	synchronous := prepared != nil && prepared.synchronous
	if synchronous {
		if completion.Outcome == pluginapi.RequestCompletionSucceeded {
			prepared.completed = "succeeded"
		} else {
			prepared.completed = "failed"
		}
	}
	c.mu.Unlock()
	if synchronous {
		notifyPrepared(prepared)
		return
	}
	if completion.Outcome != pluginapi.RequestCompletionSucceeded {
		c.finalizeRequest(completion.RequestID, "failed", usagePayload{})
		return
	}
	c.mu.Lock()
	prepared = c.prepared[completion.RequestID]
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
	var synchronous *preparedDispatch
	for candidateID, item := range c.requests {
		if item.ticket == ticket {
			prepared := c.prepared[candidateID]
			if prepared != nil && prepared.synchronous {
				if !record.Failed {
					prepared.usage.add(usagePayloadFrom(record.Detail))
				}
				synchronous = prepared
				break
			}
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
	if synchronous != nil {
		notifyPrepared(synchronous)
		return
	}
	if requestID != "" && completed == "succeeded" && usage.hasUsage() {
		c.finalizeRequest(requestID, "succeeded", usage)
	}
}

func (c *coordinator) cleanupSynchronous(requestID string) {
	c.mu.Lock()
	delete(c.requests, requestID)
	delete(c.prepared, requestID)
	c.mu.Unlock()
}

func notifyPrepared(prepared *preparedDispatch) {
	if prepared == nil || prepared.notify == nil {
		return
	}
	select {
	case prepared.notify <- struct{}{}:
	default:
	}
}

func (c *coordinator) waitSynchronousUsage(ctx context.Context, prepared *preparedDispatch, timeout time.Duration) (usagePayload, error) {
	if prepared == nil {
		return usagePayload{}, errors.New("CPA synchronous dispatch is unavailable")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		c.mu.Lock()
		usage, completed := prepared.usage, prepared.completed
		c.mu.Unlock()
		if usage.hasUsage() {
			return usage, nil
		}
		if completed == "failed" {
			return usagePayload{}, errors.New("CPA upstream request failed")
		}
		select {
		case <-prepared.notify:
		case <-timer.C:
			return usagePayload{}, errors.New("CPA upstream usage was not reported before timeout")
		case <-ctx.Done():
			return usagePayload{}, ctx.Err()
		}
	}
}

func (c *coordinator) synchronousUsage(prepared *preparedDispatch) usagePayload {
	if prepared == nil {
		return usagePayload{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return prepared.usage
}

func (c *coordinator) cleanupSynchronousByPointer(prepared *preparedDispatch) {
	if prepared == nil {
		return
	}
	c.mu.Lock()
	for requestID, candidate := range c.prepared {
		if candidate == prepared {
			delete(c.requests, requestID)
			delete(c.prepared, requestID)
			break
		}
	}
	c.mu.Unlock()
}

func preparedModel(raw []byte) string {
	var dispatch executeDispatchResponse
	if json.Unmarshal(raw, &dispatch) != nil {
		return ""
	}
	return dispatch.Model
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
	for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "X-MadAPI-Codex-Login-Mode"} {
		if value := strings.TrimSpace(src.Get(name)); value != "" {
			dst.Set(name, value)
		}
	}
}

func rewriteDispatchModel(raw []byte, model string) ([]byte, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, errors.New("NewAPI dispatch omitted model")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode CPA request for model dispatch: %w", err)
	}
	payload["model"], _ = json.Marshal(model)
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode CPA request for model dispatch: %w", err)
	}
	return rewritten, nil
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
