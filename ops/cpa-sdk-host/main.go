package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/binary"
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
	"sync/atomic"
	"time"

	cliproxy "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const (
	dispatchTokenHeader = "X-MadAPI-CPA-SDK-Token"
	metadataLimit       = 1 << 20
	payloadLimit        = 64 << 20
	defaultListenAddr   = ":18417"
	defaultInternalPort = 18317
	compatProvider      = "openai-compatible-madapi-selected-channel"
)

type dispatchMeta struct {
	ChannelType int         `json:"channel_type"`
	ChannelID   int         `json:"channel_id"`
	UserID      int         `json:"user_id"`
	BaseURL     string      `json:"base_url"`
	APIKey      string      `json:"api_key"`
	Model       string      `json:"model"`
	Headers     http.Header `json:"headers,omitempty"`
	Stream      bool        `json:"stream"`
	Compact     bool        `json:"compact"`
	Source      string      `json:"source_format"`
	RequestPath string      `json:"request_path"`
}

type codexOAuthKey struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
}

type sdkRuntime struct {
	manager *cliproxyauth.Manager
	service *cliproxy.Service
	cancel  context.CancelFunc
}

var requestSequence atomic.Uint64

func main() {
	token := strings.TrimSpace(os.Getenv("MADAPI_CPA_SDK_DISPATCH_TOKEN"))
	if len(token) < 32 {
		panic("MADAPI_CPA_SDK_DISPATCH_TOKEN must contain at least 32 characters")
	}
	runtime, err := newSDKRuntime()
	if err != nil {
		panic(err)
	}
	defer runtime.cancel()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"runtime":"official-cpa-sdk-v7.2.128"}`))
	})
	mux.Handle("/execute", dispatchHandler(runtime, token))
	addr := strings.TrimSpace(os.Getenv("MADAPI_CPA_SDK_LISTEN"))
	if addr == "" {
		addr = defaultListenAddr
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		panic(err)
	}
}

func newSDKRuntime() (*sdkRuntime, error) {
	root, err := os.MkdirTemp("", "madapi-cpa-sdk-host-")
	if err != nil {
		return nil, err
	}
	authDir := filepath.Join(root, "auths")
	if err = os.MkdirAll(authDir, 0o700); err != nil {
		return nil, err
	}
	port := defaultInternalPort
	if raw := strings.TrimSpace(os.Getenv("MADAPI_CPA_SDK_INTERNAL_PORT")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 65535 {
			return nil, fmt.Errorf("invalid MADAPI_CPA_SDK_INTERNAL_PORT %q", raw)
		}
		port = parsed
	}
	configPath := filepath.Join(root, "config.yaml")
	if err = os.WriteFile(configPath, []byte(bootstrapConfig(port, authDir)), 0o600); err != nil {
		return nil, err
	}
	cfg, err := sdkconfig.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	service, err := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		WithCoreAuthManager(manager).
		WithHooks(cliproxy.Hooks{OnAfterStart: func(*cliproxy.Service) { close(ready) }}).
		Build()
	if err != nil {
		cancel()
		return nil, err
	}
	errCh := make(chan error, 1)
	go func() { errCh <- service.Run(ctx) }()
	select {
	case <-ready:
	case runErr := <-errCh:
		cancel()
		return nil, runErr
	case <-time.After(15 * time.Second):
		cancel()
		return nil, fmt.Errorf("CPA SDK initialization timed out")
	}
	for _, provider := range []string{"codex", "claude", "gemini", "xai", compatProvider} {
		if _, ok := manager.Executor(provider); !ok {
			cancel()
			return nil, fmt.Errorf("CPA SDK executor %q is unavailable", provider)
		}
	}
	return &sdkRuntime{manager: manager, service: service, cancel: cancel}, nil
}

func dispatchHandler(runtime *sdkRuntime, expectedToken string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !privateRemote(r.RemoteAddr) || !constantTimeToken(expectedToken, r.Header.Get(dispatchTokenHeader)) {
			http.NotFound(w, r)
			return
		}
		meta, payload, err := readFrame(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if meta.Model == "" || meta.BaseURL == "" || meta.APIKey == "" || len(payload) == 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("channel, model and payload are required"))
			return
		}
		if err = executeDispatch(r.Context(), w, runtime, meta, payload); err != nil {
			writeError(w, statusCode(err), err)
		}
	})
}

func readFrame(body io.Reader) (dispatchMeta, []byte, error) {
	var meta dispatchMeta
	var prefix [4]byte
	if _, err := io.ReadFull(body, prefix[:]); err != nil {
		return meta, nil, fmt.Errorf("read metadata length: %w", err)
	}
	metadataLength := int(binary.BigEndian.Uint32(prefix[:]))
	if metadataLength < 2 || metadataLength > metadataLimit {
		return meta, nil, fmt.Errorf("invalid metadata length %d", metadataLength)
	}
	metadata := make([]byte, metadataLength)
	if _, err := io.ReadFull(body, metadata); err != nil {
		return meta, nil, fmt.Errorf("read metadata: %w", err)
	}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		return meta, nil, fmt.Errorf("decode metadata: %w", err)
	}
	payload, err := io.ReadAll(io.LimitReader(body, payloadLimit+1))
	if err != nil {
		return meta, nil, err
	}
	if len(payload) > payloadLimit {
		return meta, nil, fmt.Errorf("payload exceeds %d bytes", payloadLimit)
	}
	return meta, payload, nil
}

func executeDispatch(ctx context.Context, w http.ResponseWriter, runtime *sdkRuntime, meta dispatchMeta, payload []byte) error {
	auth, provider, err := requestAuth(meta)
	if err != nil {
		return err
	}
	auth.ID = fmt.Sprintf("madapi-request-%d-%d", meta.ChannelID, requestSequence.Add(1))
	registered, err := runtime.manager.Register(cliproxyauth.WithSkipPersist(ctx), auth)
	if err != nil {
		return err
	}
	defer runtime.manager.Remove(context.Background(), registered.ID)

	modelRegistry := cliproxy.GlobalModelRegistry()
	modelRegistry.RegisterClient(registered.ID, provider, []*cliproxy.ModelInfo{requestModelInfo(meta, provider)})
	defer modelRegistry.UnregisterClient(registered.ID)

	format := sdktranslator.FromString(meta.Source)
	metadata := map[string]any{
		cliproxyexecutor.PinnedAuthMetadataKey:     registered.ID,
		cliproxyexecutor.RequestedModelMetadataKey: meta.Model,
		cliproxyexecutor.RequestPathMetadataKey:    meta.RequestPath,
		cliproxyexecutor.CallerScopeMetadataKey:    fmt.Sprintf("madapi-user-%d", meta.UserID),
	}
	req := cliproxyexecutor.Request{Model: meta.Model, Payload: payload, Format: format, Metadata: metadata}
	opts := cliproxyexecutor.Options{
		Stream:          meta.Stream,
		Headers:         meta.Headers.Clone(),
		OriginalRequest: bytes.Clone(payload),
		SourceFormat:    format,
		ResponseFormat:  format,
		Metadata:        metadata,
	}
	if meta.Compact {
		opts.Alt = "responses/compact"
	}
	if meta.Stream {
		result, executeErr := runtime.manager.ExecuteStream(ctx, []string{provider}, req, opts)
		if executeErr != nil {
			return executeErr
		}
		copyHeaders(w.Header(), result.Headers)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				return chunk.Err
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			_, _ = w.Write(chunk.Payload)
			if !bytes.HasSuffix(chunk.Payload, []byte("\n")) {
				_, _ = w.Write([]byte("\n"))
			}
			_, _ = w.Write([]byte("\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		return nil
	}
	result, err := runtime.manager.Execute(ctx, []string{provider}, req, opts)
	if err != nil {
		return err
	}
	copyHeaders(w.Header(), result.Headers)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Payload)
	return nil
}

func requestModelInfo(meta dispatchMeta, provider string) *cliproxy.ModelInfo {
	modelType := provider
	outputModalities := []string{"TEXT"}
	if meta.Source == "openai-image" {
		modelType = "openai-image"
		outputModalities = []string{"IMAGE"}
	}
	return &cliproxy.ModelInfo{
		ID:                        meta.Model,
		Object:                    "model",
		OwnedBy:                   provider,
		Type:                      modelType,
		SupportedOutputModalities: outputModalities,
		UserDefined:               true,
		IsCompat:                  provider == compatProvider,
	}
}

func requestAuth(meta dispatchMeta) (*cliproxyauth.Auth, string, error) {
	provider := providerForChannel(meta.ChannelType)
	attributes := map[string]string{
		"api_key":      meta.APIKey,
		"base_url":     normalizedBaseURL(meta.BaseURL, provider),
		"auth_kind":    "api",
		"runtime_only": "true",
	}
	var metadata map[string]any
	if provider == "xai" {
		attributes["using_api"] = "true"
	}
	if provider == "codex" && strings.HasPrefix(strings.TrimSpace(meta.APIKey), "{") {
		var key codexOAuthKey
		if err := json.Unmarshal([]byte(meta.APIKey), &key); err != nil {
			return nil, "", fmt.Errorf("invalid Codex OAuth channel key")
		}
		key.AccessToken = strings.TrimSpace(key.AccessToken)
		key.AccountID = strings.TrimSpace(key.AccountID)
		if key.AccessToken == "" || key.AccountID == "" {
			return nil, "", fmt.Errorf("incomplete Codex OAuth channel key")
		}
		delete(attributes, "api_key")
		attributes["auth_kind"] = "oauth"
		metadata = map[string]any{"access_token": key.AccessToken, "account_id": key.AccountID}
	}
	for name, values := range meta.Headers {
		if len(values) == 0 {
			continue
		}
		value := strings.TrimSpace(values[len(values)-1])
		if strings.TrimSpace(name) != "" && value != "" {
			attributes["header:"+name] = value
		}
	}
	now := time.Now()
	return &cliproxyauth.Auth{
		Provider:   provider,
		Status:     cliproxyauth.StatusActive,
		Attributes: attributes,
		Metadata:   metadata,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, provider, nil
}

func providerForChannel(channelType int) string {
	switch channelType {
	case 14:
		return "claude"
	case 24:
		return "gemini"
	case 48:
		return "xai"
	case 57:
		return "codex"
	default:
		return compatProvider
	}
}

func normalizedBaseURL(raw, provider string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(raw), "/")
	if (provider == "xai" || provider == compatProvider) && baseURL != "" && !strings.HasSuffix(strings.ToLower(baseURL), "/v1") {
		baseURL += "/v1"
	}
	return baseURL
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

func statusCode(err error) int {
	var statusErr interface{ StatusCode() int }
	if errors.As(err, &statusErr) {
		if status := statusErr.StatusCode(); status >= 400 && status <= 599 {
			return status
		}
	}
	return http.StatusBadGateway
}

func writeError(w http.ResponseWriter, status int, err error) {
	body := []byte(strings.TrimSpace(err.Error()))
	if json.Valid(body) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"type": "cpa_sdk_error", "message": err.Error(),
	}})
}

func copyHeaders(dst, src http.Header) {
	for name, values := range src {
		switch strings.ToLower(name) {
		case "connection", "content-length", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func bootstrapConfig(port int, authDir string) string {
	return fmt.Sprintf(`host: "127.0.0.1"
port: %d
auth-dir: %s
api-keys:
  - "madapi-cpa-sdk-internal-unused"
debug: false
request-log: false
usage-statistics-enabled: false
request-retry: 0
max-retry-credentials: 1
max-retry-interval: 0
disable-cooling: true
disable-image-generation: "passthrough"
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
    base-url: "http://127.0.0.1:9/v1"
    models:
      - name: "madapi-bootstrap-unused"
openai-compatibility:
  - name: "madapi-selected-channel"
    base-url: "http://127.0.0.1:9/v1"
    api-key-entries:
      - api-key: "madapi-bootstrap-unused"
    models:
      - name: "madapi-bootstrap-unused"
        image: true
`, port, strconv.Quote(filepath.ToSlash(authDir)))
}
