package controller

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

// videoProxyError returns a standardized OpenAI-style error response.
func videoProxyError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": message,
			"type":    errType,
		},
	})
}

func copyVideoRangeHeaders(dst, src http.Header) {
	for _, key := range []string{"Range", "If-Range"} {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}

func copyVideoResponseHeaders(dst, src http.Header) {
	for _, key := range []string{
		"Accept-Ranges",
		"Content-Length",
		"Content-Range",
		"Content-Type",
		"ETag",
		"Last-Modified",
	} {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}

func parseSingleByteRange(value string, total int64) (int64, int64, bool) {
	if total <= 0 || !strings.HasPrefix(value, "bytes=") {
		return 0, 0, false
	}
	value = strings.TrimSpace(strings.TrimPrefix(value, "bytes="))
	if value == "" || strings.Contains(value, ",") {
		return 0, 0, false
	}
	parts := strings.SplitN(value, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	if parts[0] == "" {
		suffix, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, false
		}
		if suffix > total {
			suffix = total
		}
		return total - suffix, total - 1, true
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= total {
		return 0, 0, false
	}
	end := total - 1
	if parts[1] != "" {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return 0, 0, false
		}
		if end >= total {
			end = total - 1
		}
	}
	return start, end, true
}

func videoSignature(key, taskID, expires string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(taskID + "\n" + expires))
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func validateSignedVideoURL(taskID, expires, signature string, tokens []*model.Token, now int64) bool {
	expiresAt, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || expiresAt < now || expiresAt > now+15*60 {
		return false
	}
	for _, token := range tokens {
		if token == nil || token.Status == common.TokenStatusDisabled {
			continue
		}
		expected := videoSignature(token.Key, taskID, expires)
		if hmac.Equal([]byte(expected), []byte(signature)) {
			return true
		}
	}
	return false
}

func getVideoTaskForRequest(c *gin.Context, taskID string) (*model.Task, bool, error) {
	expires := c.Query("expires")
	signature := c.Query("signature")
	if expires == "" || signature == "" {
		return model.GetByTaskId(c.GetInt("id"), taskID)
	}
	task, exists, err := model.GetByTaskIdAnyUser(taskID)
	if err != nil || !exists || task == nil {
		return task, exists, err
	}
	tokens, err := model.GetAllUserTokens(task.UserId, 0, 1000)
	if err != nil {
		return nil, false, err
	}
	if !validateSignedVideoURL(taskID, expires, signature, tokens, time.Now().Unix()) {
		return nil, false, nil
	}
	return task, true, nil
}

func storedVideoResultURL(task *model.Task) string {
	if task == nil {
		return ""
	}
	for _, candidate := range []string{task.PrivateData.ResultURL, task.FailReason} {
		candidate = strings.TrimSpace(candidate)
		if strings.HasPrefix(candidate, "https://") ||
			strings.HasPrefix(candidate, "http://") ||
			strings.HasPrefix(candidate, "data:") {
			return candidate
		}
	}
	return ""
}

func resolveXaiVideoResultURL(baseURL, resultURL string) (string, error) {
	resultURL = strings.TrimSpace(resultURL)
	if !strings.HasPrefix(resultURL, "/") || strings.HasPrefix(resultURL, "//") {
		return "", fmt.Errorf("xAI video result URL is not an absolute path")
	}

	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid xAI channel base URL")
	}
	reference, err := url.Parse(resultURL)
	if err != nil || reference.Host != "" {
		return "", fmt.Errorf("invalid xAI video result URL")
	}
	return base.ResolveReference(reference).String(), nil
}

func VideoProxy(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		videoProxyError(c, http.StatusBadRequest, "invalid_request_error", "task_id is required")
		return
	}

	task, exists, err := getVideoTaskForRequest(c, taskID)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to query task %s: %s", taskID, err.Error()))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to query task")
		return
	}
	if !exists || task == nil {
		videoProxyError(c, http.StatusNotFound, "invalid_request_error", "Task not found")
		return
	}

	if task.Status != model.TaskStatusSuccess {
		videoProxyError(c, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("Task is not completed yet, current status: %s", task.Status))
		return
	}

	channel, err := model.CacheGetChannel(task.ChannelId)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to get channel for task %s: %s", taskID, err.Error()))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to retrieve channel information")
		return
	}
	baseURL := channel.GetBaseURL()
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}

	var videoURL string
	resolveJSONContent := false
	proxy := channel.GetSetting().Proxy
	client := service.GetSSRFProtectedHTTPClient()
	if proxy != "" {
		// 渠道代理路径的连接由代理侧建立，无法做拨号时逐 IP 校验，
		// 因此后面对 videoURL 保留请求前的一次性 SSRF 校验。
		client, err = service.GetHttpClientWithProxy(proxy)
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to create proxy client for task %s: %s", taskID, err.Error()))
			videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to create proxy client")
			return
		}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "", nil)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to create request: %s", err.Error()))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to create proxy request")
		return
	}

	if resultURL := storedVideoResultURL(task); resultURL != "" {
		videoURL = resultURL
	} else if channel.Type == constant.ChannelTypeXai {
		videoURL, err = resolveXaiVideoResultURL(baseURL, task.GetResultURL())
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to resolve xAI video URL for task %s: %s", taskID, err.Error()))
			videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to resolve xAI video URL")
			return
		}
		req.Header.Set("Authorization", "Bearer "+channel.Key)
	} else if task.Platform == constant.TaskPlatformApiOkSeedance {
		videoURL = fmt.Sprintf("%s/v1/videos/%s/content", baseURL, task.GetUpstreamTaskID())
		req.Header.Set("Authorization", "Bearer "+channel.Key)
		resolveJSONContent = true
	} else {
		switch channel.Type {
		case constant.ChannelTypeGemini:
			apiKey := task.PrivateData.Key
			if apiKey == "" {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Missing stored API key for Gemini task %s", taskID))
				videoProxyError(c, http.StatusInternalServerError, "server_error", "API key not stored for task")
				return
			}
			videoURL, err = getGeminiVideoURL(channel, task, apiKey)
			if err != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to resolve Gemini video URL for task %s: %s", taskID, err.Error()))
				videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to resolve Gemini video URL")
				return
			}
			req.Header.Set("x-goog-api-key", apiKey)
		case constant.ChannelTypeVertexAi:
			videoURL, err = getVertexVideoURL(channel, task)
			if err != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to resolve Vertex video URL for task %s: %s", taskID, err.Error()))
				videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to resolve Vertex video URL")
				return
			}
		case constant.ChannelTypeOpenAI, constant.ChannelTypeSora:
			videoURL = fmt.Sprintf("%s/v1/videos/%s/content", baseURL, task.GetUpstreamTaskID())
			req.Header.Set("Authorization", "Bearer "+channel.Key)
			resolveJSONContent = true
		default:
			// Video URL is stored in PrivateData.ResultURL (fallback to FailReason for old data)
			videoURL = task.GetResultURL()
		}
	}

	videoURL = strings.TrimSpace(videoURL)
	if videoURL == "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Video URL is empty for task %s", taskID))
		videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch video content")
		return
	}

	if strings.HasPrefix(videoURL, "data:") {
		if err := writeVideoDataURL(c, videoURL); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to decode video data URL for task %s: %s", taskID, err.Error()))
			videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch video content")
		}
		return
	}

	if validateErr := validateVideoProxyURL(videoURL, proxy); validateErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Video URL blocked for task %s: %v", taskID, validateErr))
		videoProxyError(c, http.StatusForbidden, "server_error", fmt.Sprintf("request blocked: %v", validateErr))
		return
	}

	req.URL, err = url.Parse(videoURL)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to parse URL %s: %s", videoURL, err.Error()))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to create proxy request")
		return
	}
	copyVideoRangeHeaders(req.Header, c.Request.Header)

	resp, err := client.Do(req)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to fetch video from %s: %s", videoURL, err.Error()))
		videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch video content")
		return
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		logger.LogError(c.Request.Context(), fmt.Sprintf("Upstream returned status %d for %s", resp.StatusCode, videoURL))
		videoProxyError(c, http.StatusBadGateway, "server_error",
			fmt.Sprintf("Upstream service returned status %d", resp.StatusCode))
		return
	}

	if resolveJSONContent && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to read video metadata for task %s: %s", taskID, readErr.Error()))
			videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to resolve video content")
			return
		}

		resolvedURL := extractVideoURLFromContentResponse(body)
		if resolvedURL == "" {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Video metadata contains no URL for task %s: %s", taskID, string(body)))
			videoProxyError(c, http.StatusBadGateway, "server_error", "Upstream returned no video URL")
			return
		}
		if validateErr := validateVideoProxyURL(resolvedURL, proxy); validateErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Resolved video URL blocked for task %s: %v", taskID, validateErr))
			videoProxyError(c, http.StatusForbidden, "server_error", fmt.Sprintf("request blocked: %v", validateErr))
			return
		}

		resolvedReq, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, resolvedURL, nil)
		if requestErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to create resolved video request for task %s: %s", taskID, requestErr.Error()))
			videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to create video request")
			return
		}
		copyVideoRangeHeaders(resolvedReq.Header, c.Request.Header)

		resp, err = client.Do(resolvedReq)
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to fetch resolved video for task %s: %s", taskID, err.Error()))
			videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch video content")
			return
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			logger.LogError(c.Request.Context(), fmt.Sprintf("Resolved video returned status %d for task %s", resp.StatusCode, taskID))
			videoProxyError(c, http.StatusBadGateway, "server_error", fmt.Sprintf("Video service returned status %d", resp.StatusCode))
			return
		}
	}
	defer resp.Body.Close()

	copyVideoResponseHeaders(c.Writer.Header(), resp.Header)
	if c.Writer.Header().Get("Content-Type") == "" && channel.Type == constant.ChannelTypeXai {
		c.Writer.Header().Set("Content-Type", "video/mp4")
	}
	c.Writer.Header().Set("Cache-Control", "private, no-store")
	if c.Query("download") == "1" {
		c.Writer.Header().Set(
			"Content-Disposition",
			fmt.Sprintf("attachment; filename=\"madapi-video-%s.mp4\"", taskID),
		)
	} else {
		c.Writer.Header().Set("Content-Disposition", "inline")
	}
	responseStatus := resp.StatusCode
	copyLength := int64(-1)
	if resp.StatusCode == http.StatusOK && c.GetHeader("Range") != "" && resp.ContentLength > 0 {
		start, end, ok := parseSingleByteRange(c.GetHeader("Range"), resp.ContentLength)
		if !ok {
			c.Writer.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", resp.ContentLength))
			c.Writer.Header().Set("Content-Length", "0")
			c.Status(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if start > 0 {
			if _, discardErr := io.CopyN(io.Discard, resp.Body, start); discardErr != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to seek video stream: %s", discardErr.Error()))
				return
			}
		}
		copyLength = end - start + 1
		responseStatus = http.StatusPartialContent
		c.Writer.Header().Set("Accept-Ranges", "bytes")
		c.Writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, resp.ContentLength))
		c.Writer.Header().Set("Content-Length", strconv.FormatInt(copyLength, 10))
	}
	c.Writer.WriteHeader(responseStatus)
	if copyLength >= 0 {
		_, err = io.CopyN(c.Writer, resp.Body, copyLength)
	} else {
		_, err = io.Copy(c.Writer, resp.Body)
	}
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to stream video content: %s", err.Error()))
	}
}

func validateVideoProxyURL(videoURL, proxy string) error {
	if proxy == "" {
		return service.ValidateSSRFProtectedFetchURL(videoURL)
	}
	fetchSetting := system_setting.GetFetchSetting()
	return common.ValidateURLWithFetchSetting(videoURL, fetchSetting.EnableSSRFProtection, fetchSetting.AllowPrivateIp, fetchSetting.DomainFilterMode, fetchSetting.IpFilterMode, fetchSetting.DomainList, fetchSetting.IpList, fetchSetting.AllowedPorts, fetchSetting.ApplyIPFilterForDomain)
}

func extractVideoURLFromContentResponse(body []byte) string {
	var payload struct {
		URL   string `json:"url"`
		Video struct {
			URL string `json:"url"`
		} `json:"video"`
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := common.Unmarshal(body, &payload); err != nil {
		return ""
	}
	for _, candidate := range []string{payload.Video.URL, payload.URL, payload.Data.URL} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func writeVideoDataURL(c *gin.Context, dataURL string) error {
	parts := strings.SplitN(dataURL, ",", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid data url")
	}

	header := parts[0]
	payload := parts[1]
	if !strings.HasPrefix(header, "data:") || !strings.Contains(header, ";base64") {
		return fmt.Errorf("unsupported data url")
	}

	mimeType := strings.TrimPrefix(header, "data:")
	mimeType = strings.TrimSuffix(mimeType, ";base64")
	if mimeType == "" {
		mimeType = "video/mp4"
	}

	videoBytes, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		videoBytes, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return err
		}
	}

	c.Writer.Header().Set("Content-Type", mimeType)
	c.Writer.Header().Set("Cache-Control", "public, max-age=86400")
	c.Writer.WriteHeader(http.StatusOK)
	_, err = c.Writer.Write(videoBytes)
	return err
}
