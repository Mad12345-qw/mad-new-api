package xai

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	taskcommon "github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

const normalizedRequestKey = "xai_video_normalized_request"

type imageInput struct {
	URL string `json:"url"`
}

type generationRequest struct {
	Model           string       `json:"model"`
	Prompt          string       `json:"prompt"`
	Duration        int          `json:"duration"`
	AspectRatio     string       `json:"aspect_ratio,omitempty"`
	Resolution      string       `json:"resolution"`
	Image           *imageInput  `json:"image,omitempty"`
	ReferenceImages []imageInput `json:"reference_images,omitempty"`
}

type generationResponse struct {
	RequestID string `json:"request_id"`
	ID        string `json:"id,omitempty"`
	Status    string `json:"status,omitempty"`
	Progress  int    `json:"progress,omitempty"`
	Video     *struct {
		URL      string  `json:"url"`
		Duration float64 `json:"duration,omitempty"`
	} `json:"video,omitempty"`
	Error any `json:"error,omitempty"`
}

type TaskAdaptor struct {
	taskcommon.BaseBilling
	apiKey  string
	baseURL string
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.apiKey = info.ApiKey
	a.baseURL = strings.TrimRight(info.ChannelBaseUrl, "/")
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	input, form, err := readCompatibleInput(c)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}

	req, err := normalizeRequest(input, form, info.OriginModelName)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	c.Set(normalizedRequestKey, req)
	info.Action = "textGenerate"
	if req.Image != nil || len(req.ReferenceImages) > 0 {
		info.Action = "generate"
	}
	return nil
}

func (a *TaskAdaptor) EstimateBilling(c *gin.Context, _ *relaycommon.RelayInfo) map[string]float64 {
	req, ok := normalizedRequest(c)
	if !ok {
		return nil
	}
	resolutionRatio := 1.0
	if req.Resolution == "1080p" {
		resolutionRatio = 25.0 / 14.0
	}
	imageCount := 0.0
	if req.Image != nil {
		imageCount = 1
	}
	imageCount += float64(len(req.ReferenceImages))
	return map[string]float64{
		"seconds":    float64(req.Duration),
		"resolution": resolutionRatio,
		"images":     imageCount,
	}
}

func (a *TaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	return a.baseURL + "/v1/videos/generations", nil
}

func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, req *http.Request, _ *relaycommon.RelayInfo) error {
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	req, ok := normalizedRequest(c)
	if !ok {
		return nil, fmt.Errorf("normalized xAI video request is missing")
	}
	req.Model = info.UpstreamModelName
	body, err := common.Marshal(req)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(body), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (string, []byte, *dto.TaskError) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	_ = resp.Body.Close()

	var upstream generationResponse
	if err := common.Unmarshal(body, &upstream); err != nil {
		return "", nil, service.TaskErrorWrapper(errors.Wrapf(err, "body: %s", body), "unmarshal_response_body_failed", http.StatusBadGateway)
	}
	upstreamID := strings.TrimSpace(upstream.RequestID)
	if upstreamID == "" {
		upstreamID = strings.TrimSpace(upstream.ID)
	}
	if upstreamID == "" {
		return "", nil, service.TaskErrorWrapper(fmt.Errorf("request_id is empty"), "invalid_response", http.StatusBadGateway)
	}

	publicResponse := map[string]any{
		"id":         info.PublicTaskID,
		"task_id":    info.PublicTaskID,
		"request_id": info.PublicTaskID,
		"object":     "video",
		"model":      info.OriginModelName,
		"status":     "queued",
		"progress":   0,
	}
	c.JSON(http.StatusOK, publicResponse)
	return upstreamID, body, nil
}

func (a *TaskAdaptor) FetchTask(baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, _ := body["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("invalid task_id")
	}
	uri := strings.TrimRight(baseURL, "/") + "/v1/videos/" + taskID
	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) ParseTaskResult(body []byte) (*relaycommon.TaskInfo, error) {
	var response generationResponse
	if err := common.Unmarshal(body, &response); err != nil {
		return nil, errors.Wrap(err, "unmarshal xAI task result failed")
	}
	result := &relaycommon.TaskInfo{}
	switch strings.ToLower(strings.TrimSpace(response.Status)) {
	case "", "pending", "queued", "submitted":
		result.Status = model.TaskStatusQueued
	case "processing", "in_progress", "running":
		result.Status = model.TaskStatusInProgress
	case "done", "completed", "succeeded", "success":
		result.Status = model.TaskStatusSuccess
		if response.Video != nil {
			result.Url = strings.TrimSpace(response.Video.URL)
		}
	case "failed", "expired", "cancelled", "canceled":
		result.Status = model.TaskStatusFailure
		result.Reason = errorMessage(response.Error)
		if result.Reason == "" {
			result.Reason = "xAI video task " + response.Status
		}
	default:
		return nil, fmt.Errorf("unknown xAI video task status: %s", response.Status)
	}
	if response.Progress > 0 && response.Progress < 100 {
		result.Progress = fmt.Sprintf("%d%%", response.Progress)
	}
	return result, nil
}

func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	video := dto.NewOpenAIVideo()
	video.ID = task.TaskID
	video.TaskID = task.TaskID
	video.Model = task.Properties.OriginModelName
	video.Status = task.Status.ToVideoStatus()
	video.SetProgressStr(task.Progress)
	video.CreatedAt = task.CreatedAt
	if task.FinishTime > 0 {
		video.CompletedAt = task.FinishTime
	}
	if task.Status == model.TaskStatusSuccess {
		video.SetMetadata("url", taskcommon.BuildProxyURL(task.TaskID))
	}
	if task.Status == model.TaskStatusFailure {
		video.Error = &dto.OpenAIVideoError{Message: task.FailReason, Code: "video_generation_failed"}
	}
	return common.Marshal(video)
}

func (a *TaskAdaptor) GetModelList() []string { return ModelList }
func (a *TaskAdaptor) GetChannelName() string { return ChannelName }

func normalizedRequest(c *gin.Context) (generationRequest, bool) {
	value, ok := c.Get(normalizedRequestKey)
	if !ok {
		return generationRequest{}, false
	}
	req, ok := value.(generationRequest)
	return req, ok
}

func readCompatibleInput(c *gin.Context) (map[string]any, *multipart.Form, error) {
	contentType := strings.ToLower(c.GetHeader("Content-Type"))
	if strings.Contains(contentType, "multipart/form-data") {
		form, err := common.ParseMultipartFormReusable(c)
		if err != nil {
			return nil, nil, err
		}
		input := make(map[string]any, len(form.Value))
		for key, values := range form.Value {
			if len(values) == 1 {
				input[key] = values[0]
			} else if len(values) > 1 {
				items := make([]any, len(values))
				for i := range values {
					items[i] = values[i]
				}
				input[key] = items
			}
		}
		return input, form, nil
	}

	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, nil, err
	}
	body, err := storage.Bytes()
	if err != nil {
		return nil, nil, err
	}
	var input map[string]any
	if err := common.Unmarshal(body, &input); err != nil {
		return nil, nil, err
	}
	var officialReferences struct {
		ReferenceImages []imageInput `json:"reference_images"`
	}
	if err := common.Unmarshal(body, &officialReferences); err == nil && officialReferences.ReferenceImages != nil {
		input["reference_images"] = officialReferences.ReferenceImages
	}
	return input, nil, nil
}

func normalizeRequest(input map[string]any, form *multipart.Form, fallbackModel string) (generationRequest, error) {
	modelName := stringValue(firstValue(input, "model", "model_name", "modelName"))
	if modelName == "" {
		modelName = strings.TrimSpace(fallbackModel)
	}
	if !IsVideoModel(modelName) {
		return generationRequest{}, fmt.Errorf("unsupported xAI video model: %s", modelName)
	}

	prompt := stringValue(firstValue(input, "prompt", "text"))
	if prompt == "" {
		prompt = extractText(firstValue(input, "input", "content", "messages"))
	}
	if strings.TrimSpace(prompt) == "" {
		return generationRequest{}, fmt.Errorf("prompt is required")
	}

	durationValue := firstValueWithMetadata(input, "duration", "seconds", "duration_seconds", "durationSeconds")
	duration := 5
	if durationValue != nil {
		var err error
		duration, err = parseInteger(durationValue)
		if err != nil {
			return generationRequest{}, fmt.Errorf("duration must be an integer from 1 to 15 seconds")
		}
	}
	if duration < 1 || duration > 15 {
		return generationRequest{}, fmt.Errorf("duration must be an integer from 1 to 15 seconds")
	}

	resolutionRaw := stringValue(firstValueWithMetadata(input, "resolution", "output_resolution", "outputResolution"))
	size := stringValue(firstValueWithMetadata(input, "size"))
	if resolutionRaw == "" && looksLikeResolution(size) {
		resolutionRaw = size
	}
	resolution, err := normalizeResolution(resolutionRaw)
	if err != nil {
		return generationRequest{}, err
	}
	if !IsVideo15Model(modelName) && resolution == "1080p" {
		return generationRequest{}, fmt.Errorf("grok-imagine-video only offers 720p on this site")
	}

	aspectRatio := stringValue(firstValueWithMetadata(input, "aspect_ratio", "aspectRatio", "ratio"))
	if aspectRatio == "" {
		aspectRatio = aspectRatioFromSize(size)
	}

	singleImages := extractImageURLs(firstValue(input, "image", "image_url", "imageUrl", "input_reference", "inputImage", "input_image"))
	referenceImages := extractImageURLs(firstValue(input, "reference_images", "referenceImages", "reference_image_urls", "referenceImageUrls"))
	genericImages := extractImageURLs(firstValue(input, "images"))
	contentImages := extractImageURLs(firstValue(input, "input", "content", "messages"))

	if form != nil {
		fileSingles, fileErr := multipartImageDataURLs(form, "image", "image_file", "file", "input_reference", "input_image")
		if fileErr != nil {
			return generationRequest{}, fileErr
		}
		fileReferences, fileErr := multipartImageDataURLs(form, "reference_images", "reference_image", "reference_image_urls", "referenceImageUrls")
		if fileErr != nil {
			return generationRequest{}, fileErr
		}
		fileGeneric, fileErr := multipartImageDataURLs(form, "images")
		if fileErr != nil {
			return generationRequest{}, fileErr
		}
		singleImages = appendUniqueStrings(singleImages, fileSingles...)
		referenceImages = appendUniqueStrings(referenceImages, fileReferences...)
		genericImages = appendUniqueStrings(genericImages, fileGeneric...)
	}

	if len(singleImages) == 0 && len(referenceImages) == 0 {
		candidateImages := appendUniqueStrings(genericImages, contentImages...)
		if len(candidateImages) == 1 {
			singleImages = candidateImages
		} else if len(candidateImages) > 1 {
			referenceImages = candidateImages
		}
	}
	if len(singleImages) > 1 {
		if len(referenceImages) > 0 {
			return generationRequest{}, fmt.Errorf("image and reference_images cannot be combined")
		}
		referenceImages = singleImages
		singleImages = nil
	}
	if len(singleImages) > 0 && len(referenceImages) > 0 {
		return generationRequest{}, fmt.Errorf("image and reference_images cannot be combined")
	}
	if len(referenceImages) > 0 {
		if !strings.EqualFold(strings.TrimSpace(modelName), "grok-imagine-video") {
			return generationRequest{}, fmt.Errorf("reference_images are only supported by grok-imagine-video")
		}
		if len(referenceImages) > 7 {
			return generationRequest{}, fmt.Errorf("reference_images supports at most 7 images")
		}
		if duration > 10 {
			return generationRequest{}, fmt.Errorf("reference_images supports a maximum duration of 10 seconds")
		}
	}
	if IsVideo15Model(modelName) && resolution == "1080p" && len(singleImages) == 0 {
		return generationRequest{}, fmt.Errorf("1080p requires an input image for grok-imagine-video-1.5")
	}

	req := generationRequest{
		Model:       modelName,
		Prompt:      strings.TrimSpace(prompt),
		Duration:    duration,
		AspectRatio: aspectRatio,
		Resolution:  resolution,
	}
	if len(singleImages) == 1 {
		req.Image = &imageInput{URL: singleImages[0]}
	}
	for _, imageURL := range referenceImages {
		req.ReferenceImages = append(req.ReferenceImages, imageInput{URL: imageURL})
	}
	return req, nil
}

func firstValue(input map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := input[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func firstValueWithMetadata(input map[string]any, keys ...string) any {
	if value := firstValue(input, keys...); value != nil {
		return value
	}
	if metadata, ok := input["metadata"].(map[string]any); ok {
		return firstValue(metadata, keys...)
	}
	if metadataJSON, ok := input["metadata"].(string); ok && strings.TrimSpace(metadataJSON) != "" {
		var metadata map[string]any
		if common.Unmarshal([]byte(metadataJSON), &metadata) == nil {
			return firstValue(metadata, keys...)
		}
	}
	return nil
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func parseInteger(value any) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case float64:
		if v == float64(int(v)) {
			return int(v), nil
		}
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("not an integer")
}

func extractText(value any) string {
	var texts []string
	var walk func(any)
	walk = func(current any) {
		switch item := current.(type) {
		case string:
			if text := strings.TrimSpace(item); text != "" {
				texts = append(texts, text)
			}
		case []any:
			for _, child := range item {
				walk(child)
			}
		case map[string]any:
			typeName := strings.ToLower(stringValue(item["type"]))
			if typeName == "text" || typeName == "input_text" || typeName == "prompt" {
				walk(item["text"])
				walk(item["content"])
				return
			}
			for _, key := range []string{"prompt", "text", "content", "input", "messages"} {
				if child, ok := item[key]; ok {
					walk(child)
				}
			}
		}
	}
	walk(value)
	return strings.Join(texts, "\n")
}

func extractImageURLs(value any) []string {
	var images []string
	var walk func(any)
	walk = func(current any) {
		switch item := current.(type) {
		case string:
			trimmed := strings.TrimSpace(item)
			if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "data:image/") {
				images = appendUniqueStrings(images, trimmed)
				return
			}
			if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
				var decoded any
				if common.Unmarshal([]byte(trimmed), &decoded) == nil {
					walk(decoded)
				}
			}
		case []any:
			for _, child := range item {
				walk(child)
			}
		case []string:
			for _, child := range item {
				walk(child)
			}
		case []map[string]any:
			for _, child := range item {
				walk(child)
			}
		case []imageInput:
			for _, child := range item {
				walk(child.URL)
			}
		case map[string]any:
			typeName := strings.ToLower(stringValue(item["type"]))
			for _, key := range []string{"image_url", "image", "input_image", "input_reference"} {
				walk(item[key])
			}
			if strings.Contains(typeName, "image") || typeName == "" {
				walk(item["url"])
			}
			for _, key := range []string{"content", "input", "messages", "images", "reference_images"} {
				walk(item[key])
			}
		}
	}
	walk(value)
	if len(images) == 0 && value != nil {
		serialized, err := common.Marshal(value)
		if err == nil {
			var normalized any
			if common.Unmarshal(serialized, &normalized) == nil {
				walk(normalized)
			}
		}
	}
	return images
}

func multipartImageDataURLs(form *multipart.Form, keys ...string) ([]string, error) {
	var images []string
	for _, key := range keys {
		for _, header := range form.File[key] {
			file, err := header.Open()
			if err != nil {
				return nil, err
			}
			data, readErr := io.ReadAll(file)
			_ = file.Close()
			if readErr != nil {
				return nil, readErr
			}
			contentType := header.Header.Get("Content-Type")
			if contentType == "" || contentType == "application/octet-stream" {
				contentType = http.DetectContentType(data)
			}
			if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
				continue
			}
			images = append(images, "data:"+contentType+";base64,"+base64.StdEncoding.EncodeToString(data))
		}
	}
	return images, nil
}

func appendUniqueStrings(existing []string, values ...string) []string {
	seen := make(map[string]struct{}, len(existing)+len(values))
	for _, value := range existing {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		existing = append(existing, value)
	}
	return existing
}

func normalizeResolution(value string) (string, error) {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
	if normalized == "" {
		return "720p", nil
	}
	switch normalized {
	case "720", "720p", "1280x720", "720x1280":
		return "720p", nil
	case "1080", "1080p", "1920x1080", "1080x1920":
		return "1080p", nil
	case "480", "480p", "854x480", "480x854":
		return "", fmt.Errorf("480p is not offered; choose 720p or 1080p")
	default:
		return "", fmt.Errorf("unsupported resolution %q; choose 720p or 1080p", value)
	}
}

func looksLikeResolution(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return strings.HasSuffix(normalized, "p") || strings.Contains(normalized, "x")
}

func aspectRatioFromSize(value string) string {
	normalized := strings.TrimSpace(value)
	if strings.Contains(normalized, ":") {
		return normalized
	}
	switch strings.ToLower(strings.ReplaceAll(normalized, " ", "")) {
	case "1280x720", "1920x1080":
		return "16:9"
	case "720x1280", "1080x1920":
		return "9:16"
	default:
		return ""
	}
}

func errorMessage(value any) string {
	switch item := value.(type) {
	case string:
		return strings.TrimSpace(item)
	case map[string]any:
		for _, key := range []string{"message", "detail", "error", "code"} {
			if text := stringValue(item[key]); text != "" {
				return text
			}
		}
	}
	return ""
}
