package sora

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

const (
	mikotoSeedanceBodyKey       = "mikoto_seedance_body"
	mikotoMultipartMaxFileBytes = int64(50 * 1024 * 1024)
	mikotoMultipartMaxBodyBytes = int64(96 * 1024 * 1024)
)

var mikotoAspectRatios = map[string]bool{
	"16:9": true,
	"9:16": true,
	"1:1":  true,
	"4:3":  true,
	"3:4":  true,
}

type mikotoSeedanceRequest struct {
	Model           string   `json:"model"`
	Prompt          string   `json:"prompt"`
	Duration        int      `json:"duration"`
	AspectRatio     string   `json:"aspect_ratio,omitempty"`
	Images          []string `json:"images,omitempty"`
	ReferenceMode   string   `json:"reference_mode,omitempty"`
	ReferenceVideos []string `json:"referenceVideos,omitempty"`
	ReferenceAudios []string `json:"referenceAudios,omitempty"`
	GenerateAudio   *bool    `json:"generate_audio,omitempty"`
}

func IsMikotoSeedanceModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "seedance-2.0-1080p",
		"seedance-2.0-720p",
		"seedance-fast-720p":
		return true
	default:
		return false
	}
}

func prepareMikotoSeedanceRequest(c *gin.Context, info *relaycommon.RelayInfo) error {
	body, request, err := normalizeMikotoSeedanceRequest(c, info.UpstreamModelName)
	if err != nil {
		return err
	}
	c.Set(mikotoSeedanceBodyKey, body)
	c.Set("task_request", request)
	info.Action = constant.TaskActionTextGenerate
	if len(request.Images) > 0 {
		info.Action = constant.TaskActionGenerate
	}
	return nil
}

func normalizeMikotoSeedanceRequest(c *gin.Context, upstreamModel string) ([]byte, relaycommon.TaskSubmitReq, error) {
	input, err := mikotoSeedanceInputMap(c)
	if err != nil {
		return nil, relaycommon.TaskSubmitReq{}, err
	}

	normalized, err := normalizeMikotoSeedanceMap(input, upstreamModel)
	if err != nil {
		return nil, relaycommon.TaskSubmitReq{}, err
	}
	body, err := common.Marshal(normalized)
	if err != nil {
		return nil, relaycommon.TaskSubmitReq{}, fmt.Errorf("marshal normalized Seedance request: %w", err)
	}

	request := relaycommon.TaskSubmitReq{
		Prompt:   normalized.Prompt,
		Model:    normalized.Model,
		Duration: normalized.Duration,
		Seconds:  strconv.Itoa(normalized.Duration),
		Images:   append([]string(nil), normalized.Images...),
	}
	return body, request, nil
}

func mikotoSeedanceInputMap(c *gin.Context) (map[string]interface{}, error) {
	contentType := strings.ToLower(c.GetHeader("Content-Type"))
	if strings.Contains(contentType, "multipart/form-data") {
		return mikotoSeedanceMultipartMap(c)
	}

	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, fmt.Errorf("read Seedance request body: %w", err)
	}
	body, err := storage.Bytes()
	if err != nil {
		return nil, fmt.Errorf("read Seedance request bytes: %w", err)
	}
	var input map[string]interface{}
	if err := common.Unmarshal(body, &input); err != nil {
		return nil, fmt.Errorf("Seedance request must be JSON or multipart form: %w", err)
	}
	return input, nil
}

func mikotoSeedanceMultipartMap(c *gin.Context) (map[string]interface{}, error) {
	form, err := common.ParseMultipartFormReusable(c)
	if err != nil {
		return nil, fmt.Errorf("parse Seedance multipart request: %w", err)
	}
	input := make(map[string]interface{}, len(form.Value)+len(form.File))
	for key, values := range form.Value {
		if len(values) == 1 {
			input[key] = values[0]
		} else if len(values) > 1 {
			items := make([]interface{}, 0, len(values))
			for _, value := range values {
				items = append(items, value)
			}
			input[key] = items
		}
	}

	var totalBytes int64
	for field, files := range form.File {
		for _, file := range files {
			totalBytes += file.Size
			if totalBytes > mikotoMultipartMaxBodyBytes {
				return nil, fmt.Errorf("multipart media exceeds 96MB; use public URLs or data URIs")
			}
			dataURI, err := mikotoFileDataURI(file)
			if err != nil {
				return nil, err
			}
			appendInputValue(input, field, dataURI)
		}
	}
	return input, nil
}

func mikotoFileDataURI(file *multipart.FileHeader) (string, error) {
	if file.Size > mikotoMultipartMaxFileBytes {
		return "", fmt.Errorf("file %q exceeds 50MB; use a public URL", file.Filename)
	}
	stream, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("open multipart file %q: %w", file.Filename, err)
	}
	defer stream.Close()

	data, err := io.ReadAll(io.LimitReader(stream, mikotoMultipartMaxFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read multipart file %q: %w", file.Filename, err)
	}
	if int64(len(data)) > mikotoMultipartMaxFileBytes {
		return "", fmt.Errorf("file %q exceeds 50MB; use a public URL", file.Filename)
	}
	mimeType := strings.TrimSpace(file.Header.Get("Content-Type"))
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = http.DetectContentType(data)
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func appendInputValue(input map[string]interface{}, field string, value interface{}) {
	if existing, ok := input[field]; ok {
		switch values := existing.(type) {
		case []interface{}:
			input[field] = append(values, value)
		default:
			input[field] = []interface{}{existing, value}
		}
		return
	}
	input[field] = value
}

func normalizeMikotoSeedanceMap(input map[string]interface{}, upstreamModel string) (mikotoSeedanceRequest, error) {
	prompt := firstString(input, "prompt", "input", "text")
	images := collectFields(input, "images", "image_urls", "reference_image_urls", "image", "image_url", "input_reference")
	videos := collectFields(input, "referenceVideos", "reference_videos", "video_urls", "videos", "video_url")
	audios := collectFields(input, "referenceAudios", "reference_audios", "audio_urls", "audios", "audio_url")

	contentPrompt, contentImages, contentVideos, contentAudios := extractMikotoContent(input["content"])
	if strings.TrimSpace(prompt) == "" {
		prompt = contentPrompt
	}
	images = appendUnique(images, contentImages...)
	videos = appendUnique(videos, contentVideos...)
	audios = appendUnique(audios, contentAudios...)

	duration, err := mikotoDuration(input)
	if err != nil {
		return mikotoSeedanceRequest{}, err
	}
	aspectRatio, err := mikotoAspectRatio(input)
	if err != nil {
		return mikotoSeedanceRequest{}, err
	}
	if strings.TrimSpace(prompt) == "" {
		return mikotoSeedanceRequest{}, fmt.Errorf("prompt is required")
	}
	if len(images) > 9 {
		return mikotoSeedanceRequest{}, fmt.Errorf("images supports at most 9 items")
	}
	if len(videos) > 3 {
		return mikotoSeedanceRequest{}, fmt.Errorf("referenceVideos supports at most 3 items")
	}
	if len(audios) > 3 {
		return mikotoSeedanceRequest{}, fmt.Errorf("referenceAudios supports at most 3 items")
	}

	referenceMode := strings.ToLower(strings.TrimSpace(firstString(input, "reference_mode", "referenceMode")))
	if referenceMode != "" && referenceMode != "frame" && referenceMode != "media" {
		return mikotoSeedanceRequest{}, fmt.Errorf("reference_mode must be frame or media")
	}
	if referenceMode == "" && len(images) >= 3 {
		referenceMode = "media"
	}

	normalized := mikotoSeedanceRequest{
		Model:           upstreamModel,
		Prompt:          prompt,
		Duration:        duration,
		AspectRatio:     aspectRatio,
		Images:          images,
		ReferenceMode:   referenceMode,
		ReferenceVideos: videos,
		ReferenceAudios: audios,
		GenerateAudio:   firstBool(input, "generate_audio", "audio"),
	}
	return normalized, nil
}

func mikotoDuration(input map[string]interface{}) (int, error) {
	value, ok := firstValue(input, "duration", "seconds", "duration_seconds", "durationSeconds")
	if !ok {
		if metadata, ok := input["metadata"].(map[string]interface{}); ok {
			value, ok = firstValue(metadata, "duration", "seconds", "duration_seconds", "durationSeconds")
		}
	}
	if !ok {
		return 0, fmt.Errorf("duration is required and must be an integer from 4 to 15")
	}

	var duration int
	switch typed := value.(type) {
	case float64:
		duration = int(typed)
		if float64(duration) != typed {
			return 0, fmt.Errorf("duration must be an integer from 4 to 15")
		}
	case int:
		duration = typed
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, fmt.Errorf("duration must be an integer from 4 to 15")
		}
		duration = parsed
	default:
		return 0, fmt.Errorf("duration must be an integer from 4 to 15")
	}
	if duration < 4 || duration > 15 {
		return 0, fmt.Errorf("duration must be between 4 and 15 seconds")
	}
	return duration, nil
}

func mikotoAspectRatio(input map[string]interface{}) (string, error) {
	ratio := strings.TrimSpace(firstString(input, "aspect_ratio", "aspectRatio", "ratio"))
	if ratio == "" {
		if metadata, ok := input["metadata"].(map[string]interface{}); ok {
			ratio = strings.TrimSpace(firstString(metadata, "aspect_ratio", "aspectRatio", "ratio"))
		}
	}
	if ratio == "" {
		ratio = aspectRatioFromSize(firstString(input, "size"))
	}
	if ratio == "" {
		ratio = "16:9"
	}
	if !mikotoAspectRatios[ratio] {
		return "", fmt.Errorf("aspect_ratio must be one of 16:9, 9:16, 1:1, 4:3, or 3:4")
	}
	return ratio, nil
}

func aspectRatioFromSize(size string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(size)), "x")
	if len(parts) != 2 {
		return ""
	}
	width, errWidth := strconv.Atoi(parts[0])
	height, errHeight := strconv.Atoi(parts[1])
	if errWidth != nil || errHeight != nil || width <= 0 || height <= 0 {
		return ""
	}
	switch {
	case width == height:
		return "1:1"
	case width*9 == height*16:
		return "16:9"
	case width*16 == height*9:
		return "9:16"
	case width*3 == height*4:
		return "4:3"
	case width*4 == height*3:
		return "3:4"
	default:
		return ""
	}
}

func extractMikotoContent(raw interface{}) (string, []string, []string, []string) {
	if text, ok := raw.(string); ok {
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "[") {
			var decoded []interface{}
			if common.Unmarshal([]byte(trimmed), &decoded) == nil {
				return extractMikotoContent(decoded)
			}
		}
		return text, nil, nil, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return "", nil, nil, nil
	}

	texts := make([]string, 0)
	var images, videos, audios []string
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			continue
		}
		itemType := strings.ToLower(strings.TrimSpace(firstString(item, "type")))
		switch itemType {
		case "text", "input_text":
			if text := firstString(item, "text", "content"); strings.TrimSpace(text) != "" {
				texts = append(texts, text)
			}
		case "image", "image_url", "input_image":
			images = appendUnique(images, extractURL(item["image_url"]), extractURL(item["image"]), extractURL(item["url"]))
		case "video", "video_url", "input_video":
			videos = appendUnique(videos, extractURL(item["video_url"]), extractURL(item["video"]), extractURL(item["url"]))
		case "audio", "audio_url", "input_audio":
			audios = appendUnique(audios, extractURL(item["audio_url"]), extractURL(item["audio"]), extractURL(item["url"]))
		}
	}
	return strings.Join(texts, "\n"), images, videos, audios
}

func collectFields(input map[string]interface{}, keys ...string) []string {
	values := make([]string, 0)
	for _, key := range keys {
		if value, ok := input[key]; ok {
			values = appendCollected(values, value)
		}
	}
	return values
}

func appendCollected(values []string, value interface{}) []string {
	switch typed := value.(type) {
	case []interface{}:
		for _, item := range typed {
			values = appendUnique(values, extractURL(item))
		}
	case []string:
		values = appendUnique(values, typed...)
	default:
		values = appendUnique(values, extractURL(typed))
	}
	return values
}

func extractURL(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]interface{}:
		for _, key := range []string{"url", "image_url", "video_url", "audio_url", "data"} {
			if nested, ok := typed[key]; ok {
				if result := extractURL(nested); result != "" {
					return result
				}
			}
		}
	}
	return ""
}

func appendUnique(values []string, candidates ...string) []string {
	seen := make(map[string]bool, len(values)+len(candidates))
	for _, value := range values {
		seen[value] = true
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		values = append(values, candidate)
	}
	return values
}

func firstString(input map[string]interface{}, keys ...string) string {
	value, ok := firstValue(input, keys...)
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

func firstValue(input map[string]interface{}, keys ...string) (interface{}, bool) {
	for _, key := range keys {
		if value, ok := input[key]; ok && value != nil {
			return value, true
		}
	}
	return nil, false
}

func firstBool(input map[string]interface{}, keys ...string) *bool {
	value, ok := firstValue(input, keys...)
	if !ok {
		return nil
	}
	var result bool
	switch typed := value.(type) {
	case bool:
		result = typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		if err != nil {
			return nil
		}
		result = parsed
	default:
		return nil
	}
	return &result
}
