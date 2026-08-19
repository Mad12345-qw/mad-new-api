package service

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/dto"
)

const geminiReferenceOrderRule = "参考图编号规则：以下图片数据块严格按照上传顺序编号。第一个图片数据块是图1，第二个是图2，依此类推。用户提示词中的“图N”只能指代相同编号的图片，不得根据图片内容重新排序、猜测编号或互换图片。"

var (
	geminiChineseReferencePattern = regexp.MustCompile(`图\s*([0-9]+)`)
	geminiEnglishReferencePattern = regexp.MustCompile(`(?i)\bimage\s*#?\s*([0-9]+)\b`)
)

// IsGeminiImagePreviewModel reports whether model belongs to the Gemini image
// preview family. Matching the family rather than a fixed version lets future
// Gemini image-preview revisions inherit the same reference-image guarantees.
func IsGeminiImagePreviewModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "gemini-") && strings.Contains(model, "-image-preview")
}

// BuildGeminiImageReferenceParts creates the canonical Gemini reference-image
// sequence shared by OpenAI-compatible image requests and native Gemini image
// requests. The media parts are never reordered.
func BuildGeminiImageReferenceParts(prompt string, mediaParts []dto.GeminiPart) []dto.GeminiPart {
	if len(mediaParts) == 0 {
		return []dto.GeminiPart{{Text: prompt}}
	}

	parts := make([]dto.GeminiPart, 0, 2+len(mediaParts)*2)
	parts = append(parts, dto.GeminiPart{Text: geminiReferenceOrderRule})
	for index, mediaPart := range mediaParts {
		position := index + 1
		parts = append(parts,
			dto.GeminiPart{Text: fmt.Sprintf("图%d（第%d个上传的参考图；紧随其后的图片数据就是图%d）", position, position, position)},
			mediaPart,
		)
	}
	parts = append(parts, dto.GeminiPart{Text: "用户原始提示词（其中“图N”严格对应以上相同编号）：\n" + prompt})
	return parts
}

// NormalizeNativeGeminiImageReferences applies the same reference ordering
// contract used by the OpenAI-compatible image endpoint to a native Gemini
// generateContent request. Only the final active user content is eligible;
// history and structured tool/function contents remain untouched. The return
// value is the number of media parts actually normalized.
func NormalizeNativeGeminiImageReferences(request *dto.GeminiChatRequest) (int, error) {
	if request == nil || len(request.Contents) == 0 {
		return 0, nil
	}

	content := &request.Contents[len(request.Contents)-1]
	if content.Role != "" && content.Role != "user" {
		return 0, nil
	}

	promptText := make([]string, 0)
	mediaParts := make([]dto.GeminiPart, 0)
	for _, part := range content.Parts {
		if strings.Contains(part.Text, geminiReferenceOrderRule) {
			return 0, nil
		}
		if part.InlineData != nil || part.FileData != nil {
			if part.Text != "" || part.FunctionCall != nil || part.FunctionResponse != nil || part.ExecutableCode != nil || part.CodeExecutionResult != nil {
				return 0, nil
			}
			mediaParts = append(mediaParts, part)
			continue
		}
		if part.FunctionCall != nil || part.FunctionResponse != nil || part.ExecutableCode != nil || part.CodeExecutionResult != nil {
			return 0, nil
		}
		if text := strings.TrimSpace(part.Text); text != "" {
			promptText = append(promptText, text)
		}
	}
	if len(mediaParts) == 0 {
		return 0, nil
	}

	joinedPrompt := strings.Join(promptText, "\n")
	maxReference := maxGeminiReferenceIndex(joinedPrompt)
	if maxReference > len(mediaParts) {
		return 0, fmt.Errorf("prompt references image %d but request contains %d reference image(s)", maxReference, len(mediaParts))
	}
	content.Parts = BuildGeminiImageReferenceParts(joinedPrompt, mediaParts)
	return len(mediaParts), nil
}

func maxGeminiReferenceIndex(prompt string) int {
	maximum := 0
	for _, pattern := range []*regexp.Regexp{geminiChineseReferencePattern, geminiEnglishReferencePattern} {
		for _, match := range pattern.FindAllStringSubmatch(prompt, -1) {
			if len(match) < 2 {
				continue
			}
			value, err := strconv.Atoi(match[1])
			if err == nil && value > maximum {
				maximum = value
			}
		}
	}
	return maximum
}
