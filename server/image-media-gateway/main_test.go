package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testGateway(t *testing.T) *gateway {
	t.Helper()
	root := t.TempDir()
	cfg := config{
		ListenAddrs: []string{"127.0.0.1:0"}, Upstream: "http://127.0.0.1:1",
		PublicBaseURL: "https://mad.test", CacheDir: filepath.Join(root, "cache"),
		SpoolDir: filepath.Join(root, "spool"), MaxImageBytes: 64 << 20,
		MaxRequestBytes: 96 << 20, MaxResponseBytes: 96 << 20,
		CacheTTL: 30 * time.Minute, CacheMaxBytes: 5 << 30,
		GlobalConcurrency: 64, URLConcurrency: 48, InlineConcurrency: 12,
		QueueTimeout: time.Second, UpstreamTimeout: time.Second, DownloadTimeout: time.Second,
	}
	gateway, err := newGateway(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return gateway
}

func TestListenAddrsPrefersPluralAndDeduplicates(t *testing.T) {
	t.Setenv("LISTEN_ADDRS", " 127.0.0.1:3013,172.22.0.1:3013,127.0.0.1:3013 ")
	t.Setenv("LISTEN_ADDR", "0.0.0.0:3012")
	got := listenAddrs()
	want := []string{"127.0.0.1:3013", "172.22.0.1:3013"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("listen addresses = %v, want %v", got, want)
	}
}

func TestListenAddrsFallsBackToSingular(t *testing.T) {
	t.Setenv("LISTEN_ADDRS", "")
	t.Setenv("LISTEN_ADDR", "127.0.0.1:4013")
	got := listenAddrs()
	if len(got) != 1 || got[0] != "127.0.0.1:4013" {
		t.Fatalf("listen addresses = %v", got)
	}
}

func TestListenAllClosesEarlierListenersOnFailure(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	firstAddr := probe.Addr().String()
	_ = probe.Close()

	if _, err := listenAll([]string{firstAddr, occupied.Addr().String()}); err == nil {
		t.Fatal("listenAll unexpectedly succeeded")
	}
	reopened, err := net.Listen("tcp", firstAddr)
	if err != nil {
		t.Fatalf("first listener was not closed: %v", err)
	}
	_ = reopened.Close()
}

func TestURLModelForcesUpstreamURL(t *testing.T) {
	g := testGateway(t)
	for _, model := range []string{
		"gpt-image-2",
		"gpt-image-2-4k",
		"grok-imagine-image",
		"grok-imagine-image-quality",
		"grok-imagine-image-pro",
	} {
		t.Run(model, func(t *testing.T) {
			body := fmt.Sprintf(`{"model":%q,"prompt":"test","response_format":"b64_json"}`, model)
			req, _ := http.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			prepared, err := g.prepare(req)
			if err != nil {
				t.Fatal(err)
			}
			defer prepared.Cleanup()
			if prepared.UpstreamPath != "/v1/images/generations" {
				t.Fatalf("upstream path = %q", prepared.UpstreamPath)
			}
			raw, _ := io.ReadAll(prepared.Body)
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["response_format"] != "url" {
				t.Fatalf("upstream format = %v", payload["response_format"])
			}
			if prepared.ClientFormat != "b64_json" {
				t.Fatalf("client format = %q", prepared.ClientFormat)
			}
		})
	}
}

func TestGeminiAliasPreservesImageRouteForChannelSelection(t *testing.T) {
	g := testGateway(t)
	models := []string{
		"gemini-3.1-flash-image-preview",
		"gemini-3.1-flash-image-preview-4K",
		"gemini-3-pro-image-preview",
		"gemini-3-pro-image-preview-4K",
	}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			body := fmt.Sprintf(`{"model":%q,"prompt":"test","response_format":"url"}`, model)
			req, _ := http.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			prepared, err := g.prepare(req)
			if err != nil {
				t.Fatal(err)
			}
			defer prepared.Cleanup()
			if prepared.UpstreamPath != "/v1/images/generations" {
				t.Fatalf("path = %q", prepared.UpstreamPath)
			}
			raw, _ := io.ReadAll(prepared.Body)
			if !bytes.Contains(raw, []byte(`"response_format":"url"`)) {
				t.Fatalf("request was not preserved: %s", raw)
			}
		})
	}
}

func TestAdaptiveGeminiMultipartEditConvertsToReplayableChat(t *testing.T) {
	models := map[string]string{
		"gemini-3.1-flash-image-preview": "1K",
		"gemini-3-pro-image-preview":     "1K",
	}
	for model, expectedSize := range models {
		t.Run(model, func(t *testing.T) {
			g := testGateway(t)
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			_ = writer.WriteField("model", model)
			_ = writer.WriteField("prompt", "edit this image")
			_ = writer.WriteField("response_format", "b64_json")
			part, _ := writer.CreateFormFile("image[]", "input.png")
			_, _ = part.Write([]byte("reference-image"))
			_ = writer.Close()

			req, _ := http.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
			req.Header.Set("Content-Type", writer.FormDataContentType())
			prepared, err := g.prepare(req)
			if err != nil {
				t.Fatal(err)
			}
			defer prepared.Cleanup()
			if prepared.UpstreamPath != "/v1/chat/completions" {
				t.Fatalf("path = %q", prepared.UpstreamPath)
			}
			if prepared.ContentType != "application/json" || prepared.Capability != modelAdaptive {
				t.Fatalf("content type = %q, capability = %q", prepared.ContentType, prepared.Capability)
			}
			raw, _ := io.ReadAll(prepared.Body)
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("invalid upstream JSON: %v: %s", err, raw)
			}
			if payload["model"] != model {
				t.Fatalf("model = %v", payload["model"])
			}
			if !bytes.Contains(raw, []byte(`"type":"image_url"`)) || !bytes.Contains(raw, []byte("data:application/octet-stream;base64,")) {
				t.Fatalf("reference image was not converted: %s", raw)
			}
			if !bytes.Contains(raw, []byte(`"image_size":"`+expectedSize+`"`)) {
				t.Fatalf("image size was not preserved: %s", raw)
			}
		})
	}
}

func TestGemini4KMultipartEditPreservesWorkingChannelRoute(t *testing.T) {
	for _, model := range []string{
		"gemini-3.1-flash-image-preview-4K",
		"gemini-3-pro-image-preview-4K",
	} {
		t.Run(model, func(t *testing.T) {
			g := testGateway(t)
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			_ = writer.WriteField("model", model)
			_ = writer.WriteField("prompt", "edit this image")
			_ = writer.WriteField("response_format", "url")
			part, _ := writer.CreateFormFile("image[]", "input.png")
			_, _ = part.Write([]byte("reference-image"))
			_ = writer.Close()

			req, _ := http.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
			req.Header.Set("Content-Type", writer.FormDataContentType())
			prepared, err := g.prepare(req)
			if err != nil {
				t.Fatal(err)
			}
			defer prepared.Cleanup()
			if prepared.UpstreamPath != "/v1/images/edits" {
				t.Fatalf("4K alias route changed to %q", prepared.UpstreamPath)
			}
			if prepared.ContentType == "application/json" || prepared.Capability != modelAdaptiveURL {
				t.Fatalf("4K alias was converted unexpectedly: content type = %q, capability = %q", prepared.ContentType, prepared.Capability)
			}
			upstream, _ := io.ReadAll(prepared.Body)
			if !bytes.Contains(upstream, []byte(model)) || !bytes.Contains(upstream, []byte("reference-image")) {
				t.Fatalf("4K multipart request was not preserved")
			}
			if !bytes.Contains(upstream, []byte("url")) || bytes.Contains(upstream, []byte("b64_json")) {
				t.Fatalf("4K alias did not force an upstream URL response")
			}
		})
	}
}

func TestGemini4KPaidSuccessWithInlineImageReturnsURL(t *testing.T) {
	rawImage := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{4}, 1024)...)
	encoded := base64.StdEncoding.EncodeToString(rawImage)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/edits" {
			t.Fatalf("upstream path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"created":1,"data":[{"b64_json":%q}]}`, encoded)
	}))
	defer upstream.Close()

	g := testGateway(t)
	g.cfg.Upstream = upstream.URL
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "gemini-3-pro-image-preview-4K")
	_ = writer.WriteField("prompt", "edit this image")
	_ = writer.WriteField("response_format", "url")
	part, _ := writer.CreateFormFile("image[]", "input.png")
	_, _ = part.Write([]byte("reference-image"))
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	g.handleImage(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response imageResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 || !strings.HasPrefix(response.Data[0].URL, "https://mad.test/image-cache/") {
		t.Fatalf("response = %s", recorder.Body.String())
	}
}

func TestImageModelMatrixConcurrentRouting(t *testing.T) {
	g := testGateway(t)
	type routeCase struct {
		model      string
		path       string
		multipart  bool
		wantPath   string
		capability string
	}
	cases := []routeCase{
		{model: "gemini-3.1-flash-image-preview", path: "/v1/images/edits", multipart: true, wantPath: "/v1/chat/completions", capability: modelAdaptive},
		{model: "gemini-3-pro-image-preview-4K", path: "/v1/images/edits", multipart: true, wantPath: "/v1/images/edits", capability: modelAdaptiveURL},
		{model: "gpt-image-2-4k", path: "/v1/images/generations", wantPath: "/v1/images/generations", capability: modelURL},
		{model: "grok-imagine-image-quality", path: "/v1/images/generations", wantPath: "/v1/images/generations", capability: modelURL},
	}

	var wait sync.WaitGroup
	errors := make(chan error, 160)
	for index := 0; index < 160; index++ {
		item := cases[index%len(cases)]
		wait.Add(1)
		go func() {
			defer wait.Done()
			var request *http.Request
			if item.multipart {
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				_ = writer.WriteField("model", item.model)
				_ = writer.WriteField("prompt", "matrix test")
				_ = writer.WriteField("response_format", "b64_json")
				part, _ := writer.CreateFormFile("image[]", "input.png")
				_, _ = part.Write([]byte("reference-image"))
				_ = writer.Close()
				request, _ = http.NewRequest(http.MethodPost, item.path, bytes.NewReader(body.Bytes()))
				request.Header.Set("Content-Type", writer.FormDataContentType())
			} else {
				body := fmt.Sprintf(`{"model":%q,"prompt":"matrix test","response_format":"b64_json"}`, item.model)
				request, _ = http.NewRequest(http.MethodPost, item.path, strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
			}

			prepared, err := g.prepare(request)
			if err != nil {
				errors <- fmt.Errorf("%s prepare: %w", item.model, err)
				return
			}
			defer prepared.Cleanup()
			if prepared.UpstreamPath != item.wantPath || prepared.Capability != item.capability {
				errors <- fmt.Errorf("%s routed to %s (%s), want %s (%s)", item.model, prepared.UpstreamPath, prepared.Capability, item.wantPath, item.capability)
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestAdaptiveGeminiUsesTheInlineConcurrencyClass(t *testing.T) {
	g := testGateway(t)
	if capabilityFor("gemini-3.1-flash-image-preview-2k") != modelAdaptive {
		t.Fatal("Gemini preview model is not adaptive")
	}
	if g.slotsForCapability(modelAdaptive) != g.inlineSlots {
		t.Fatal("adaptive Gemini did not use inline slots")
	}
	if g.slotsForCapability(modelURL) != g.urlSlots {
		t.Fatal("URL image model did not use URL slots")
	}
}

func TestMultipartURLModelForcesURLWithoutLoadingUpload(t *testing.T) {
	g := testGateway(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "gpt-image-2")
	_ = writer.WriteField("response_format", "b64_json")
	part, _ := writer.CreateFormFile("image[]", "input.png")
	_, _ = part.Write(append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{0}, 1024)...))
	_ = writer.Close()
	req, _ := http.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	prepared, err := g.prepare(req)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Cleanup()
	upstream, _ := io.ReadAll(prepared.Body)
	if !bytes.Contains(upstream, []byte("url")) || bytes.Contains(upstream, []byte("b64_json")) {
		t.Fatalf("multipart format was not normalized")
	}
}

func TestInlineBase64DecodesToCache(t *testing.T) {
	g := testGateway(t)
	rawImage := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{1}, 4<<20)...)
	encoded := base64.StdEncoding.EncodeToString(rawImage)
	response := `{"choices":[{"message":{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + encoded + `"}}]}}]}`
	path := filepath.Join(g.cfg.SpoolDir, "inline.json")
	if err := os.WriteFile(path, []byte(response), 0600); err != nil {
		t.Fatal(err)
	}
	publicURL, err := g.extractInlineImage(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(publicURL, "https://mad.test/image-cache/") {
		t.Fatalf("url = %q", publicURL)
	}
	name := filepath.Base(publicURL)
	info, err := os.Stat(filepath.Join(g.cfg.CacheDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(rawImage)) {
		t.Fatalf("size = %d", info.Size())
	}
}

func TestMarkdownInlineBase64DecodesToCache(t *testing.T) {
	g := testGateway(t)
	rawImage := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{2}, 2<<20)...)
	encoded := base64.StdEncoding.EncodeToString(rawImage)
	response := `{"choices":[{"message":{"content":"![image](data:image/png;base64,` + encoded + `)"}}]}`
	path := filepath.Join(g.cfg.SpoolDir, "markdown-inline.json")
	if err := os.WriteFile(path, []byte(response), 0600); err != nil {
		t.Fatal(err)
	}
	publicURL, err := g.extractInlineImage(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(g.cfg.CacheDir, filepath.Base(publicURL)))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(rawImage)) {
		t.Fatalf("size = %d", info.Size())
	}
}

func TestGeminiInlineDataDecodesToCache(t *testing.T) {
	g := testGateway(t)
	rawImage := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{3}, 2<<20)...)
	encoded := base64.StdEncoding.EncodeToString(rawImage)
	response := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + encoded + `"}}]}}]}`
	path := filepath.Join(g.cfg.SpoolDir, "gemini-inline.json")
	if err := os.WriteFile(path, []byte(response), 0600); err != nil {
		t.Fatal(err)
	}
	publicURL, err := g.extractInlineImage(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(g.cfg.CacheDir, filepath.Base(publicURL)))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(rawImage)) {
		t.Fatalf("size = %d", info.Size())
	}
}

func TestUnknownModelRemainsPassthrough(t *testing.T) {
	if got := capabilityFor("other-image-model"); got != "" {
		t.Fatalf("capability = %q", got)
	}
}

func TestCleanupEnforcesCacheCapAndRemovesPartials(t *testing.T) {
	g := testGateway(t)
	g.cfg.CacheMaxBytes = 2 << 20
	now := time.Now()
	for index := 0; index < 3; index++ {
		path := filepath.Join(g.cfg.CacheDir, fmt.Sprintf("image-%d.png", index))
		if err := os.WriteFile(path, bytes.Repeat([]byte{byte(index)}, 1<<20), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := now.Add(time.Duration(index) * time.Second)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	partial := filepath.Join(g.cfg.CacheDir, ".download-partial")
	if err := os.WriteFile(partial, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	stale := now.Add(-partialFileTTL - time.Minute)
	if err := os.Chtimes(partial, stale, stale); err != nil {
		t.Fatal(err)
	}

	g.cleanupCache()

	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatalf("partial file still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(g.cfg.CacheDir, "image-0.png")); !os.IsNotExist(err) {
		t.Fatalf("oldest cache file still exists: %v", err)
	}
	if got := g.cacheBytes.Load(); got != 2<<20 {
		t.Fatalf("cache bytes = %d", got)
	}
}
