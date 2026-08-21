package openai

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	madImageCacheTTL      = 30 * time.Minute
	madImageCleanupPeriod = 5 * time.Minute
	madImageMaxBytes      = int64(64 * 1024 * 1024)
	defaultMadImagePath   = "/data/mad-image-cache"
)

var (
	madImageCleanupMu   sync.Mutex
	madImageLastCleanup time.Time
	madImageLookupIP    = net.DefaultResolver.LookupIP
	madImageHTTPClient  = &http.Client{
		Timeout: 180 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many upstream image redirects")
			}
			return validateMadImageRemoteURL(req.Context(), req.URL)
		},
	}
)

// MadImageCacheEntry is a locally cached image exposed through the existing
// /mad-media/images/ static route. Remove is intended for callers that need to
// roll back a partially processed upstream response.
type MadImageCacheEntry struct {
	URL  string
	path string
}

func (entry *MadImageCacheEntry) Remove() {
	if entry == nil || entry.path == "" {
		return
	}
	_ = os.Remove(entry.path)
}

// CacheMadImageReader stores one decoded image without buffering it in memory
// and returns a same-site MadAPI URL. The caller retains ownership of reader.
func CacheMadImageReader(c *gin.Context, reader io.Reader) (*MadImageCacheEntry, error) {
	if c == nil || c.Request == nil {
		return nil, errors.New("public image request context is missing")
	}
	filename, err := cacheMadImage(reader)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(madImageCacheDir(), filename)
	base, err := madImagePublicBase(c)
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return &MadImageCacheEntry{
		URL:  base + "/mad-media/images/" + url.PathEscape(filename),
		path: path,
	}, nil
}

func shouldNormalizeMadImageURL(info *relaycommon.RelayInfo) bool {
	if info == nil || info.IsStream {
		return false
	}
	request, ok := info.Request.(*dto.ImageRequest)
	if !ok || request == nil || !strings.EqualFold(strings.TrimSpace(request.ResponseFormat), "url") {
		return false
	}
	model := strings.ToLower(strings.TrimSpace(info.OriginModelName))
	if model == "" {
		model = strings.ToLower(strings.TrimSpace(request.Model))
	}
	return model == "gpt-image-2" || model == "gpt-image-2-4k"
}

func madImageCacheDir() string {
	if value := strings.TrimSpace(os.Getenv("MADAPI_IMAGE_CACHE_DIR")); value != "" {
		return value
	}
	return defaultMadImagePath
}

func cleanupMadImageCache(now time.Time) {
	madImageCleanupMu.Lock()
	defer madImageCleanupMu.Unlock()
	if !madImageLastCleanup.IsZero() && now.Sub(madImageLastCleanup) < madImageCleanupPeriod {
		return
	}
	madImageLastCleanup = now
	entries, err := os.ReadDir(madImageCacheDir())
	if err != nil {
		return
	}
	cutoff := now.Add(-madImageCacheTTL)
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(madImageCacheDir(), entry.Name())
		if info, statErr := entry.Info(); statErr == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
}

func madImagePublicBase(c *gin.Context) (string, error) {
	if configured := strings.TrimRight(strings.TrimSpace(os.Getenv("MADAPI_PUBLIC_BASE_URL")), "/"); configured != "" {
		parsed, err := url.Parse(configured)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return "", errors.New("invalid MADAPI_PUBLIC_BASE_URL")
		}
		return configured, nil
	}
	host := strings.TrimSpace(c.Request.Host)
	if host == "" || strings.ContainsAny(host, "\r\n/\\") {
		return "", errors.New("invalid public request host")
	}
	scheme := strings.ToLower(strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0]))
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	return scheme + "://" + host, nil
}

func randomMadImageName() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func detectMadImageExtension(prefix []byte) (string, bool) {
	switch {
	case len(prefix) >= 8 && string(prefix[:8]) == "\x89PNG\r\n\x1a\n":
		return "png", true
	case len(prefix) >= 3 && prefix[0] == 0xff && prefix[1] == 0xd8 && prefix[2] == 0xff:
		return "jpg", true
	case len(prefix) >= 12 && string(prefix[:4]) == "RIFF" && string(prefix[8:12]) == "WEBP":
		return "webp", true
	case len(prefix) >= 12 && string(prefix[4:8]) == "ftyp":
		brand := string(prefix[8:12])
		if brand == "avif" || brand == "avis" || brand == "mif1" || brand == "msf1" {
			return "avif", true
		}
	}
	return "", false
}

func cacheMadImage(reader io.Reader) (string, error) {
	directory := madImageCacheDir()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	name, err := randomMadImageName()
	if err != nil {
		return "", err
	}
	tempPath := filepath.Join(directory, "."+name+".tmp")
	handle, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		_ = handle.Close()
		if !keep {
			_ = os.Remove(tempPath)
		}
	}()
	written, copyErr := io.Copy(handle, io.LimitReader(reader, madImageMaxBytes+1))
	if copyErr != nil {
		return "", copyErr
	}
	if written <= 0 || written > madImageMaxBytes {
		return "", errors.New("upstream image is empty or too large")
	}
	if err = handle.Sync(); err != nil {
		return "", err
	}
	if err = handle.Close(); err != nil {
		return "", err
	}
	prefixHandle, err := os.Open(tempPath)
	if err != nil {
		return "", err
	}
	prefix := make([]byte, 16)
	prefixLength, readErr := io.ReadFull(prefixHandle, prefix)
	_ = prefixHandle.Close()
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		return "", readErr
	}
	extension, ok := detectMadImageExtension(prefix[:prefixLength])
	if !ok {
		return "", errors.New("unsupported upstream image format")
	}
	finalName := name + "." + extension
	finalPath := filepath.Join(directory, finalName)
	if err = os.Chmod(tempPath, 0o644); err != nil {
		return "", err
	}
	if err = os.Rename(tempPath, finalPath); err != nil {
		return "", err
	}
	keep = true
	cleanupMadImageCache(time.Now())
	return finalName, nil
}

func validateMadImageRemoteURL(ctx context.Context, parsed *url.URL) error {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("invalid upstream image URL")
	}
	addresses, err := madImageLookupIP(ctx, "ip", parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return errors.New("upstream image host could not be resolved")
	}
	for _, address := range addresses {
		if address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsUnspecified() || address.IsMulticast() {
			return errors.New("upstream image URL must resolve to a public address")
		}
	}
	return nil
}

func cacheMadImageURL(ctx context.Context, value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	if err = validateMadImageRemoteURL(ctx, parsed); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	request.Header.Set("User-Agent", "Mozilla/5.0 MadAPI-Image-Cache/1.0")
	response, err := madImageHTTPClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("upstream image download returned %d", response.StatusCode)
	}
	return cacheMadImage(response.Body)
}

func madImageDataItems(ctx context.Context, responseBody []byte) ([]map[string]json.RawMessage, error) {
	data := gjson.GetBytes(responseBody, "data")
	if !data.IsArray() {
		return nil, errors.New("upstream image response has no data array")
	}
	items := make([]map[string]json.RawMessage, 0, len(data.Array()))
	for _, item := range data.Array() {
		if !item.IsObject() {
			return nil, errors.New("upstream image response contains an invalid data item")
		}
		result := make(map[string]json.RawMessage)
		item.ForEach(func(key, value gjson.Result) bool {
			if key.String() != "b64_json" && key.String() != "url" {
				result[key.String()] = append(json.RawMessage(nil), value.Raw...)
			}
			return true
		})
		var filename string
		if encoded := item.Get("b64_json"); encoded.Type == gjson.String && encoded.String() != "" {
			raw := encoded.Raw
			if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' || strings.Contains(raw, "\\") {
				raw = "\"" + encoded.String() + "\""
			}
			decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(raw[1:len(raw)-1]))
			var err error
			filename, err = cacheMadImage(decoder)
			if err != nil {
				return nil, err
			}
		} else if remote := strings.TrimSpace(item.Get("url").String()); remote != "" {
			var err error
			filename, err = cacheMadImageURL(ctx, remote)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, errors.New("upstream image response contains neither url nor b64_json")
		}
		result["url"] = json.RawMessage("null")
		result["url"] = append(result["url"][:0], '"')
		result["url"] = append(result["url"], filename...)
		result["url"] = append(result["url"], '"')
		items = append(items, result)
	}
	return items, nil
}

func normalizeMadImageURLResponse(c *gin.Context, responseBody []byte) ([]byte, error) {
	items, err := madImageDataItems(c.Request.Context(), responseBody)
	if err != nil {
		return nil, err
	}
	base, err := madImagePublicBase(c)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		var filename string
		if err = json.Unmarshal(item["url"], &filename); err != nil {
			return nil, err
		}
		publicURL, marshalErr := json.Marshal(base + "/mad-media/images/" + url.PathEscape(filename))
		if marshalErr != nil {
			return nil, marshalErr
		}
		item["url"] = publicURL
	}
	result := make(map[string]json.RawMessage)
	gjson.ParseBytes(responseBody).ForEach(func(key, value gjson.Result) bool {
		if key.String() != "data" {
			result[key.String()] = append(json.RawMessage(nil), value.Raw...)
		}
		return true
	})
	encodedItems, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	result["data"] = encodedItems
	return json.Marshal(result)
}
