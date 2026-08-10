package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	modelURL         = "url"
	modelInline      = "inline"
	modelAdaptive    = "adaptive"
	modelAdaptiveURL = "adaptive-url"
	partialFileTTL   = 10 * time.Minute
)

var modelCapabilities = map[string]string{
	"gpt-image-2":                       modelURL,
	"gpt-image-2-4k":                    modelURL,
	"grok-imagine-image":                modelURL,
	"grok-imagine-image-quality":        modelURL,
	"grok-imagine-image-pro":            modelURL,
	"gemini-3.1-flash-image-preview":    modelAdaptive,
	"gemini-3.1-flash-image-preview-4k": modelAdaptiveURL,
	"gemini-3.1-flash-image-preview-2k": modelAdaptive,
	"gemini-3-pro-image-preview":        modelAdaptive,
	"gemini-3-pro-image-preview-4k":     modelAdaptiveURL,
}

type config struct {
	ListenAddrs       []string
	Upstream          string
	PublicBaseURL     string
	CacheDir          string
	SpoolDir          string
	MaxImageBytes     int64
	MaxRequestBytes   int64
	MaxResponseBytes  int64
	CacheTTL          time.Duration
	CacheMaxBytes     int64
	GlobalConcurrency int
	URLConcurrency    int
	InlineConcurrency int
	QueueTimeout      time.Duration
	UpstreamTimeout   time.Duration
	DownloadTimeout   time.Duration
}

type gateway struct {
	cfg         config
	client      *http.Client
	download    *http.Client
	globalSlots chan struct{}
	urlSlots    chan struct{}
	inlineSlots chan struct{}
	active      atomic.Int64
	served      atomic.Int64
	failed      atomic.Int64
	cacheBytes  atomic.Int64
	cleanupRun  atomic.Bool
	cleanupMu   sync.Mutex
}

type preparedRequest struct {
	Body          io.ReadCloser
	ContentLength int64
	ContentType   string
	Model         string
	ClientFormat  string
	Capability    string
	UpstreamPath  string
	Cleanup       func()
}

type multipartUpload struct {
	Field       string
	Filename    string
	ContentType string
	Path        string
}

type spooledMultipart struct {
	Fields  map[string][]string
	Uploads []multipartUpload
	Cleanup func()
}

type imageItem struct {
	URL     string `json:"url,omitempty"`
	B64JSON string `json:"b64_json,omitempty"`
}

type imageResponse struct {
	Created int64       `json:"created,omitempty"`
	Data    []imageItem `json:"data"`
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(env(name, strconv.Itoa(fallback)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envInt64(name string, fallback int64) int64 {
	value, err := strconv.ParseInt(env(name, strconv.FormatInt(fallback, 10)), 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func loadConfig() config {
	return config{
		ListenAddrs:       listenAddrs(),
		Upstream:          strings.TrimRight(env("UPSTREAM", "http://127.0.0.1:3001"), "/"),
		PublicBaseURL:     strings.TrimRight(env("PUBLIC_BASE_URL", "http://127.0.0.1:3010"), "/"),
		CacheDir:          env("CACHE_DIR", "/opt/image-url-cache"),
		SpoolDir:          env("SPOOL_DIR", "/tmp/madapi-image-media"),
		MaxImageBytes:     envInt64("MAX_IMAGE_BYTES", 64<<20),
		MaxRequestBytes:   envInt64("MAX_REQUEST_BYTES", 96<<20),
		MaxResponseBytes:  envInt64("MAX_RESPONSE_BYTES", 96<<20),
		CacheTTL:          time.Duration(envInt("CACHE_TTL_SECONDS", 1800)) * time.Second,
		CacheMaxBytes:     envInt64("CACHE_MAX_BYTES", 5<<30),
		GlobalConcurrency: envInt("GLOBAL_CONCURRENCY", 64),
		URLConcurrency:    envInt("URL_CONCURRENCY", 48),
		InlineConcurrency: envInt("INLINE_CONCURRENCY", 12),
		QueueTimeout:      time.Duration(envInt("QUEUE_TIMEOUT_SECONDS", 300)) * time.Second,
		UpstreamTimeout:   time.Duration(envInt("UPSTREAM_TIMEOUT_SECONDS", 360)) * time.Second,
		DownloadTimeout:   time.Duration(envInt("DOWNLOAD_TIMEOUT_SECONDS", 180)) * time.Second,
	}
}

func listenAddrs() []string {
	raw := strings.TrimSpace(os.Getenv("LISTEN_ADDRS"))
	if raw == "" {
		raw = env("LISTEN_ADDR", "127.0.0.1:3013")
	}
	seen := make(map[string]struct{})
	addrs := make([]string, 0, 2)
	for _, item := range strings.Split(raw, ",") {
		addr := strings.TrimSpace(item)
		if addr == "" {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		addrs = append(addrs, addr)
	}
	return addrs
}

func listenAll(addrs []string) ([]net.Listener, error) {
	if len(addrs) == 0 {
		return nil, errors.New("no listen addresses configured")
	}
	listeners := make([]net.Listener, 0, len(addrs))
	for _, addr := range addrs {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("listen on %s: %w", addr, err)
		}
		listeners = append(listeners, listener)
	}
	return listeners, nil
}

func newGateway(cfg config) (*gateway, error) {
	for _, dir := range []string{cfg.CacheDir, cfg.SpoolDir} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, err
		}
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          cfg.GlobalConcurrency * 2,
		MaxIdleConnsPerHost:   cfg.GlobalConcurrency,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: cfg.UpstreamTimeout,
	}
	return &gateway{
		cfg:         cfg,
		client:      &http.Client{Transport: transport, Timeout: cfg.UpstreamTimeout},
		download:    &http.Client{Transport: transport, Timeout: cfg.DownloadTimeout},
		globalSlots: make(chan struct{}, cfg.GlobalConcurrency),
		urlSlots:    make(chan struct{}, cfg.URLConcurrency),
		inlineSlots: make(chan struct{}, cfg.InlineConcurrency),
	}, nil
}

func capabilityFor(model string) string {
	return modelCapabilities[strings.ToLower(strings.TrimSpace(model))]
}

func isImagePath(path string) bool {
	switch path {
	case "/images/generations", "/v1/images/generations", "/pg/images/generations",
		"/images/edits", "/v1/images/edits", "/pg/images/edits",
		"/images/variations", "/v1/images/variations":
		return true
	default:
		return false
	}
}

func canonicalImagePath(path string) string {
	if strings.HasPrefix(path, "/pg/") {
		return path
	}
	if !strings.HasPrefix(path, "/v1/") {
		return "/v1" + path
	}
	return path
}

func chatPath(path string) string {
	if strings.HasPrefix(path, "/pg/") {
		return "/pg/chat/completions"
	}
	return "/v1/chat/completions"
}

func randomName() string {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)
}

func cloneHeader(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func copyRequestHeaders(dst, src http.Header) {
	for key, values := range src {
		switch strings.ToLower(key) {
		case "host", "content-length", "transfer-encoding", "connection":
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func acquire(ctx context.Context, timeout time.Duration, slots ...chan struct{}) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	acquired := make([]chan struct{}, 0, len(slots))
	for _, slot := range slots {
		select {
		case slot <- struct{}{}:
			acquired = append(acquired, slot)
		case <-ctx.Done():
			for index := len(acquired) - 1; index >= 0; index-- {
				<-acquired[index]
			}
			cancel()
			return nil, ctx.Err()
		}
	}
	return func() {
		for index := len(acquired) - 1; index >= 0; index-- {
			<-acquired[index]
		}
		cancel()
	}, nil
}

func spoolReader(dir, pattern string, source io.Reader, limit int64) (*os.File, int64, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, 0, err
	}
	ok := false
	defer func() {
		if !ok {
			name := file.Name()
			_ = file.Close()
			_ = os.Remove(name)
		}
	}()
	written, err := io.Copy(file, io.LimitReader(source, limit+1))
	if err != nil {
		return nil, written, err
	}
	if written > limit {
		return nil, written, fmt.Errorf("body exceeds %d bytes", limit)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, written, err
	}
	ok = true
	return file, written, nil
}

func parseJSONRequest(raw []byte) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON request: %w", err)
	}
	return payload, nil
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func imageSize(payload map[string]any) string {
	for _, key := range []string{"image_size", "output_resolution", "size"} {
		value := strings.ToUpper(stringValue(payload[key]))
		if value == "1K" || value == "2K" || value == "4K" {
			return value
		}
	}
	model := strings.ToLower(stringValue(payload["model"]))
	if strings.HasSuffix(model, "-2k") {
		return "2K"
	}
	return "1K"
}

func isImageEditPath(path string) bool {
	switch canonicalImagePath(path) {
	case "/v1/images/edits", "/pg/images/edits":
		return true
	default:
		return false
	}
}

func buildGeminiChatJSON(payload map[string]any) ([]byte, error) {
	imageConfig := map[string]any{"image_size": imageSize(payload)}
	if ratio := stringValue(payload["aspect_ratio"]); ratio != "" {
		imageConfig["aspect_ratio"] = ratio
	}
	messageContent := any(stringValue(payload["prompt"]))
	if messageContent == "" {
		messageContent = "Generate an image"
	}
	request := map[string]any{
		"model":      payload["model"],
		"messages":   []any{map[string]any{"role": "user", "content": messageContent}},
		"stream":     false,
		"extra_body": map[string]any{"google": map[string]any{"image_config": imageConfig}},
	}
	if group := stringValue(payload["group"]); group != "" {
		request["group"] = group
	}
	return json.Marshal(request)
}

func (g *gateway) prepareJSON(r *http.Request) (*preparedRequest, error) {
	file, size, err := spoolReader(g.cfg.SpoolDir, "request-json-*", r.Body, g.cfg.MaxRequestBytes)
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = file.Close(); _ = os.Remove(file.Name()) }
	raw, err := io.ReadAll(file)
	if err != nil {
		cleanup()
		return nil, err
	}
	payload, err := parseJSONRequest(raw)
	if err != nil {
		cleanup()
		return nil, err
	}
	model := stringValue(payload["model"])
	capability := capabilityFor(model)
	clientFormat := strings.ToLower(stringValue(payload["response_format"]))
	if clientFormat != "b64_json" {
		clientFormat = "url"
	}
	upstreamPath := canonicalImagePath(r.URL.Path)
	if capability == modelURL || capability == modelAdaptiveURL {
		payload["response_format"] = "url"
		raw, err = json.Marshal(payload)
	} else if capability == modelInline {
		raw, err = buildGeminiChatJSON(payload)
		upstreamPath = chatPath(r.URL.Path)
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	_ = size
	cleanup()
	return &preparedRequest{
		Body:          io.NopCloser(bytes.NewReader(raw)),
		ContentLength: int64(len(raw)),
		ContentType:   "application/json",
		Model:         model,
		ClientFormat:  clientFormat,
		Capability:    capability,
		UpstreamPath:  upstreamPath,
		Cleanup:       func() {},
	}, nil
}

func copyMIMEHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func (g *gateway) spoolMultipart(r *http.Request, boundary string) (*spooledMultipart, error) {
	root, err := os.MkdirTemp(g.cfg.SpoolDir, "multipart-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	result := &spooledMultipart{Fields: make(map[string][]string), Cleanup: cleanup}
	reader := multipart.NewReader(io.LimitReader(r.Body, g.cfg.MaxRequestBytes+1), boundary)
	var total int64
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			cleanup()
			return nil, nextErr
		}
		field := part.FormName()
		if field == "" {
			_ = part.Close()
			continue
		}
		if part.FileName() == "" {
			value, readErr := io.ReadAll(io.LimitReader(part, 1<<20))
			_ = part.Close()
			if readErr != nil {
				cleanup()
				return nil, readErr
			}
			total += int64(len(value))
			result.Fields[field] = append(result.Fields[field], string(value))
			continue
		}
		path := filepath.Join(root, randomName()+".upload")
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if createErr != nil {
			cleanup()
			return nil, createErr
		}
		written, copyErr := io.Copy(file, io.LimitReader(part, g.cfg.MaxRequestBytes-total+1))
		closeErr := file.Close()
		_ = part.Close()
		total += written
		if copyErr != nil || closeErr != nil || total > g.cfg.MaxRequestBytes {
			cleanup()
			if copyErr != nil {
				return nil, copyErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			return nil, fmt.Errorf("request exceeds %d bytes", g.cfg.MaxRequestBytes)
		}
		result.Uploads = append(result.Uploads, multipartUpload{
			Field: field, Filename: part.FileName(), ContentType: part.Header.Get("Content-Type"), Path: path,
		})
	}
	return result, nil
}

func (g *gateway) rebuildMultipart(spooled *spooledMultipart, forceURL bool) (*os.File, int64, string, error) {
	file, err := os.CreateTemp(g.cfg.SpoolDir, "multipart-upstream-*")
	if err != nil {
		return nil, 0, "", err
	}
	writer := multipart.NewWriter(file)
	for key, values := range spooled.Fields {
		if forceURL && key == "response_format" {
			continue
		}
		for _, value := range values {
			if err := writer.WriteField(key, value); err != nil {
				_ = writer.Close()
				_ = file.Close()
				_ = os.Remove(file.Name())
				return nil, 0, "", err
			}
		}
	}
	if forceURL {
		if err := writer.WriteField("response_format", "url"); err != nil {
			_ = writer.Close()
			_ = file.Close()
			_ = os.Remove(file.Name())
			return nil, 0, "", err
		}
	}
	for _, upload := range spooled.Uploads {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, upload.Field, upload.Filename))
		if upload.ContentType != "" {
			header.Set("Content-Type", upload.ContentType)
		}
		part, err := writer.CreatePart(copyMIMEHeader(header))
		if err != nil {
			_ = writer.Close()
			_ = file.Close()
			_ = os.Remove(file.Name())
			return nil, 0, "", err
		}
		source, err := os.Open(upload.Path)
		if err != nil {
			_ = writer.Close()
			_ = file.Close()
			_ = os.Remove(file.Name())
			return nil, 0, "", err
		}
		_, copyErr := io.Copy(part, source)
		_ = source.Close()
		if copyErr != nil {
			_ = writer.Close()
			_ = file.Close()
			_ = os.Remove(file.Name())
			return nil, 0, "", copyErr
		}
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, 0, "", err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, 0, "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, 0, "", err
	}
	return file, info.Size(), writer.FormDataContentType(), nil
}

func (g *gateway) buildGeminiMultipart(spooled *spooledMultipart) (*os.File, int64, error) {
	file, err := os.CreateTemp(g.cfg.SpoolDir, "gemini-chat-*")
	if err != nil {
		return nil, 0, err
	}
	fail := func(err error) (*os.File, int64, error) {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, 0, err
	}
	field := func(name string) string {
		if values := spooled.Fields[name]; len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
		return ""
	}
	model := field("model")
	prompt := field("prompt")
	if prompt == "" {
		prompt = "Generate an image"
	}
	imageSize := strings.ToUpper(field("image_size"))
	if imageSize == "" {
		imageSize = strings.ToUpper(field("output_resolution"))
	}
	if imageSize != "1K" && imageSize != "2K" && imageSize != "4K" {
		quality := strings.ToUpper(field("quality"))
		if strings.HasSuffix(strings.ToLower(model), "-2k") || quality == "HD" || quality == "HIGH" {
			imageSize = "2K"
		} else {
			imageSize = "1K"
		}
	}
	modelJSON, _ := json.Marshal(model)
	promptJSON, _ := json.Marshal(prompt)
	ratioJSON, _ := json.Marshal(field("aspect_ratio"))
	if _, err := fmt.Fprintf(file, `{"model":%s,"messages":[{"role":"user","content":[{"type":"text","text":%s}`, modelJSON, promptJSON); err != nil {
		return fail(err)
	}
	for _, upload := range spooled.Uploads {
		mediaType := upload.ContentType
		if mediaType == "" {
			mediaType = "image/png"
		}
		if _, err := fmt.Fprintf(file, `,{"type":"image_url","image_url":{"url":"data:%s;base64,`, mediaType); err != nil {
			return fail(err)
		}
		source, err := os.Open(upload.Path)
		if err != nil {
			return fail(err)
		}
		encoder := base64.NewEncoder(base64.StdEncoding, file)
		_, copyErr := io.Copy(encoder, source)
		closeEncodeErr := encoder.Close()
		_ = source.Close()
		if copyErr != nil {
			return fail(copyErr)
		}
		if closeEncodeErr != nil {
			return fail(closeEncodeErr)
		}
		if _, err := io.WriteString(file, `"}}`); err != nil {
			return fail(err)
		}
	}
	if _, err := fmt.Fprintf(file, `]}],"stream":false,"extra_body":{"google":{"image_config":{"image_size":%q`, imageSize); err != nil {
		return fail(err)
	}
	if field("aspect_ratio") != "" {
		if _, err := fmt.Fprintf(file, `,"aspect_ratio":%s`, ratioJSON); err != nil {
			return fail(err)
		}
	}
	if _, err := io.WriteString(file, `}}}}`); err != nil {
		return fail(err)
	}
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return file, info.Size(), nil
}

func (g *gateway) prepareMultipart(r *http.Request, contentType string) (*preparedRequest, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return nil, errors.New("multipart boundary is missing")
	}
	spooled, err := g.spoolMultipart(r, params["boundary"])
	if err != nil {
		return nil, err
	}
	field := func(name string) string {
		if values := spooled.Fields[name]; len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
		return ""
	}
	model := field("model")
	capability := capabilityFor(model)
	clientFormat := strings.ToLower(field("response_format"))
	if clientFormat != "b64_json" {
		clientFormat = "url"
	}
	var body *os.File
	var size int64
	upstreamType := ""
	upstreamPath := canonicalImagePath(r.URL.Path)
	if capability == modelInline || (capability == modelAdaptive && isImageEditPath(r.URL.Path)) {
		body, size, err = g.buildGeminiMultipart(spooled)
		upstreamType = "application/json"
		upstreamPath = chatPath(r.URL.Path)
	} else {
		body, size, upstreamType, err = g.rebuildMultipart(spooled, capability == modelURL || capability == modelAdaptiveURL)
	}
	if err != nil {
		spooled.Cleanup()
		return nil, err
	}
	return &preparedRequest{
		Body: body, ContentLength: size, ContentType: upstreamType, Model: model,
		ClientFormat: clientFormat, Capability: capability, UpstreamPath: upstreamPath,
		Cleanup: func() { _ = body.Close(); _ = os.Remove(body.Name()); spooled.Cleanup() },
	}, nil
}

func (g *gateway) prepare(r *http.Request) (*preparedRequest, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(contentType), "multipart/") {
		return g.prepareMultipart(r, contentType)
	}
	return g.prepareJSON(r)
}

func copyResponseHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		switch strings.ToLower(key) {
		case "content-length", "content-encoding", "transfer-encoding", "connection":
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func imageType(prefix []byte) (string, string, bool) {
	switch {
	case len(prefix) >= 8 && bytes.Equal(prefix[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return "png", "image/png", true
	case len(prefix) >= 3 && prefix[0] == 0xff && prefix[1] == 0xd8 && prefix[2] == 0xff:
		return "jpg", "image/jpeg", true
	case len(prefix) >= 12 && string(prefix[:4]) == "RIFF" && string(prefix[8:12]) == "WEBP":
		return "webp", "image/webp", true
	case len(prefix) >= 6 && (string(prefix[:6]) == "GIF87a" || string(prefix[:6]) == "GIF89a"):
		return "gif", "image/gif", true
	default:
		return "", "", false
	}
}

func (g *gateway) finishImage(tempPath string, prefix []byte) (string, string, error) {
	ext, contentType, ok := imageType(prefix)
	if !ok {
		return "", "", errors.New("unsupported image format")
	}
	info, err := os.Stat(tempPath)
	if err != nil {
		return "", "", err
	}
	name := randomName() + "." + ext
	finalPath := filepath.Join(g.cfg.CacheDir, name)
	if err := os.Chmod(tempPath, 0640); err != nil {
		return "", "", err
	}
	g.cleanupMu.Lock()
	if err := os.Rename(tempPath, finalPath); err != nil {
		g.cleanupMu.Unlock()
		return "", "", err
	}
	total := g.cacheBytes.Add(info.Size())
	g.cleanupMu.Unlock()
	if total > g.cfg.CacheMaxBytes {
		g.requestCleanup()
	}
	return g.cfg.PublicBaseURL + "/image-cache/" + name, contentType, nil
}

func (g *gateway) cacheHTTPImage(ctx context.Context, sourceURL string) (string, error) {
	parsed, err := url.Parse(sourceURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("invalid upstream image URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "MadAPI-Image-Media/1.0")
	resp, err := g.download.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("image download returned %d", resp.StatusCode)
	}
	file, err := os.CreateTemp(g.cfg.CacheDir, ".download-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	prefix := make([]byte, 0, 256)
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > g.cfg.MaxImageBytes {
				return "", errors.New("upstream image is too large")
			}
			if len(prefix) < cap(prefix) {
				need := cap(prefix) - len(prefix)
				if n < need {
					need = n
				}
				prefix = append(prefix, buffer[:need]...)
			}
			if _, err := file.Write(buffer[:n]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	publicURL, _, err := g.finishImage(path, prefix)
	if err != nil {
		return "", err
	}
	keep = true
	return publicURL, nil
}

type quotedBase64Reader struct {
	reader *bufio.Reader
	done   bool
}

func (q *quotedBase64Reader) Read(buffer []byte) (int, error) {
	if q.done {
		return 0, io.EOF
	}
	written := 0
	for written < len(buffer) {
		b, err := q.reader.ReadByte()
		if err != nil {
			if written > 0 {
				return written, nil
			}
			return 0, err
		}
		if b == '"' || b == ')' {
			q.done = true
			if written > 0 {
				return written, nil
			}
			return 0, io.EOF
		}
		if b == '\\' {
			next, err := q.reader.ReadByte()
			if err != nil {
				return written, err
			}
			if next == '/' {
				b = '/'
			} else {
				return written, errors.New("escaped base64 data is unsupported")
			}
		}
		if b == '\r' || b == '\n' || b == ' ' || b == '\t' {
			continue
		}
		if !isBase64Byte(b) {
			return written, fmt.Errorf("invalid base64 byte 0x%02x", b)
		}
		buffer[written] = b
		written++
	}
	return written, nil
}

func isBase64Byte(value byte) bool {
	return value >= 'A' && value <= 'Z' ||
		value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' ||
		value == '+' || value == '/' || value == '='
}

func findMarker(reader *bufio.Reader, marker []byte) error {
	matched := 0
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return err
		}
		if b == marker[matched] {
			matched++
			if matched == len(marker) {
				return nil
			}
			continue
		}
		if b == marker[0] {
			matched = 1
		} else {
			matched = 0
		}
	}
}

func (g *gateway) decodeBase64AfterMarker(sourcePath string, marker []byte) (string, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer source.Close()
	reader := bufio.NewReaderSize(source, 64<<10)
	if err := findMarker(reader, marker); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(g.cfg.CacheDir, ".base64-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	decoder := base64.NewDecoder(base64.StdEncoding, &quotedBase64Reader{reader: reader})
	prefix := make([]byte, 0, 256)
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		n, readErr := decoder.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > g.cfg.MaxImageBytes {
				return "", errors.New("upstream image is too large")
			}
			if len(prefix) < cap(prefix) {
				need := cap(prefix) - len(prefix)
				if n < need {
					need = n
				}
				prefix = append(prefix, buffer[:need]...)
			}
			if _, err := file.Write(buffer[:n]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	publicURL, _, err := g.finishImage(path, prefix)
	if err != nil {
		return "", err
	}
	keep = true
	return publicURL, nil
}

func (g *gateway) extractInlineImage(sourcePath string) (string, error) {
	markers := [][]byte{
		[]byte("base64,"),
		[]byte(`"b64_json":"`),
		[]byte(`"b64_json": "`),
		[]byte(`"data":"`),
		[]byte(`"data": "`),
	}
	var last error
	for _, marker := range markers {
		url, err := g.decodeBase64AfterMarker(sourcePath, marker)
		if err == nil {
			return url, nil
		}
		last = err
	}
	return "", fmt.Errorf("image data not found: %w", last)
}

func (g *gateway) spoolResponse(body io.Reader) (string, int64, error) {
	file, size, err := spoolReader(g.cfg.SpoolDir, "upstream-response-*", body, g.cfg.MaxResponseBytes)
	if err != nil {
		return "", size, err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", size, err
	}
	return path, size, nil
}

func parseURLResponse(path string) (imageResponse, error) {
	file, err := os.Open(path)
	if err != nil {
		return imageResponse{}, err
	}
	defer file.Close()
	var payload imageResponse
	decoder := json.NewDecoder(io.LimitReader(file, 2<<20))
	if err := decoder.Decode(&payload); err != nil {
		return imageResponse{}, err
	}
	if len(payload.Data) == 0 || payload.Data[0].URL == "" {
		return imageResponse{}, errors.New("upstream URL response has no image")
	}
	return payload, nil
}

func (g *gateway) writeURLResponse(w http.ResponseWriter, urls []string) {
	items := make([]imageItem, 0, len(urls))
	for _, item := range urls {
		items = append(items, imageItem{URL: item})
	}
	payload, _ := json.Marshal(imageResponse{Created: time.Now().Unix(), Data: items})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.Header().Set("X-MadAPI-Image-Mode", "url")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (g *gateway) writeBase64Response(w http.ResponseWriter, publicURL string) error {
	parsed, err := url.Parse(publicURL)
	if err != nil {
		return err
	}
	name := filepath.Base(parsed.Path)
	path := filepath.Join(g.cfg.CacheDir, name)
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-MadAPI-Image-Mode", "b64_json-stream")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, fmt.Sprintf(`{"created":%d,"data":[{"b64_json":"`, time.Now().Unix())); err != nil {
		return err
	}
	encoder := base64.NewEncoder(base64.StdEncoding, w)
	if _, err := io.CopyBuffer(encoder, file, make([]byte, 64<<10)); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	_, err = io.WriteString(w, `"}]}`)
	return err
}

func (g *gateway) serveImage(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(strings.TrimPrefix(r.URL.Path, "/image-cache/"))
	if name == "." || name == "" || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(g.cfg.CacheDir, name)
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	prefix := make([]byte, 16)
	n, _ := io.ReadFull(file, prefix)
	_, contentType, ok := imageType(prefix[:n])
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = file.Seek(0, io.SeekStart)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Cache-Control", "public, max-age=1800, immutable")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (g *gateway) slotsForCapability(capability string) chan struct{} {
	if capability == modelInline || capability == modelAdaptive {
		return g.inlineSlots
	}
	return g.urlSlots
}

func (g *gateway) handleImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	prepared, err := g.prepare(r)
	if err != nil {
		g.failed.Add(1)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer prepared.Cleanup()
	slots := g.slotsForCapability(prepared.Capability)
	release, err := acquire(r.Context(), g.cfg.QueueTimeout, g.globalSlots, slots)
	if err != nil {
		g.failed.Add(1)
		http.Error(w, "image queue timed out", http.StatusServiceUnavailable)
		return
	}
	defer release()
	g.active.Add(1)
	defer g.active.Add(-1)
	upstreamURL := g.cfg.Upstream + prepared.UpstreamPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, prepared.Body)
	if err != nil {
		g.failed.Add(1)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	req.ContentLength = prepared.ContentLength
	copyRequestHeaders(req.Header, r.Header)
	req.Header.Set("Content-Type", prepared.ContentType)
	resp, err := g.client.Do(req)
	if err != nil {
		g.failed.Add(1)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		g.failed.Add(1)
		copyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.CopyBuffer(w, io.LimitReader(resp.Body, g.cfg.MaxResponseBytes), make([]byte, 64<<10))
		return
	}
	path, _, err := g.spoolResponse(resp.Body)
	if err != nil {
		g.failed.Add(1)
		http.Error(w, "upstream image response is too large", http.StatusBadGateway)
		return
	}
	defer os.Remove(path)
	var publicURL string
	if prepared.Capability == modelInline {
		publicURL, err = g.extractInlineImage(path)
	} else if prepared.Capability == modelAdaptive || prepared.Capability == modelAdaptiveURL {
		payload, parseErr := parseURLResponse(path)
		if parseErr == nil {
			publicURL, err = g.cacheHTTPImage(r.Context(), payload.Data[0].URL)
		} else {
			publicURL, err = g.extractInlineImage(path)
		}
	} else {
		payload, parseErr := parseURLResponse(path)
		if parseErr != nil {
			err = parseErr
		} else {
			publicURL, err = g.cacheHTTPImage(r.Context(), payload.Data[0].URL)
		}
	}
	if err != nil {
		g.failed.Add(1)
		http.Error(w, "upstream image response is invalid", http.StatusBadGateway)
		return
	}
	if prepared.ClientFormat == "b64_json" {
		if err := g.writeBase64Response(w, publicURL); err != nil {
			g.failed.Add(1)
		} else {
			g.served.Add(1)
		}
		return
	}
	g.writeURLResponse(w, []string{publicURL})
	g.served.Add(1)
}

func (g *gateway) requestCleanup() {
	if !g.cleanupRun.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer g.cleanupRun.Store(false)
		g.cleanupCache()
	}()
}

func (g *gateway) cleanupCache() {
	g.cleanupMu.Lock()
	defer g.cleanupMu.Unlock()
	entries, err := os.ReadDir(g.cfg.CacheDir)
	if err != nil {
		return
	}
	type item struct {
		path string
		mod  time.Time
		size int64
	}
	items := make([]item, 0, len(entries))
	var total int64
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(g.cfg.CacheDir, entry.Name())
		if strings.HasPrefix(entry.Name(), ".") {
			if now.Sub(info.ModTime()) > partialFileTTL {
				_ = os.Remove(path)
			}
			continue
		}
		if now.Sub(info.ModTime()) > g.cfg.CacheTTL {
			_ = os.Remove(path)
			continue
		}
		items = append(items, item{path: path, mod: info.ModTime(), size: info.Size()})
		total += info.Size()
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for index := 0; total > g.cfg.CacheMaxBytes && index < len(items); index++ {
		if err := os.Remove(items[index].path); err == nil {
			total -= items[index].size
		}
	}
	g.cacheBytes.Store(total)
}

func (g *gateway) health(w http.ResponseWriter, _ *http.Request) {
	payload, _ := json.Marshal(map[string]any{"ok": true, "active": g.active.Load(), "served": g.served.Load(), "failed": g.failed.Load()})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

func (g *gateway) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", g.health)
	mux.HandleFunc("/image-cache/", g.serveImage)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if isImagePath(r.URL.Path) {
			g.handleImage(w, r)
			return
		}
		http.NotFound(w, r)
	})
	return mux
}

func main() {
	cfg := loadConfig()
	gateway, err := newGateway(cfg)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			gateway.cleanupCache()
		}
	}()
	gateway.cleanupCache()
	listeners, err := listenAll(cfg.ListenAddrs)
	if err != nil {
		log.Fatal(err)
	}
	handler := gateway.handler()
	serveErrors := make(chan error, len(listeners))
	for _, listener := range listeners {
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second}
		log.Printf("image media gateway listening on %s", listener.Addr())
		go func(server *http.Server, listener net.Listener) {
			serveErrors <- server.Serve(listener)
		}(server, listener)
	}
	err = <-serveErrors
	for _, listener := range listeners {
		_ = listener.Close()
	}
	log.Fatal(err)
}
