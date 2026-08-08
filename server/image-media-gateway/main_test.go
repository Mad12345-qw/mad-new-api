package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testGateway(t *testing.T) *gateway {
	t.Helper()
	root := t.TempDir()
	cfg := config{
		ListenAddr: "127.0.0.1:0", Upstream: "http://127.0.0.1:1",
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

func TestURLModelForcesUpstreamURL(t *testing.T) {
	g := testGateway(t)
	for _, model := range []string{"gpt-image-2", "grok-imagine-image"} {
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
	body := `{"model":"gemini-3.1-flash-image-preview-2k","prompt":"test","response_format":"url"}`
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
