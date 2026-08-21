package gemini

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	systemconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const (
	geminiImagePreviewMaxResponseBytes = int64(96 << 20)
	geminiImagePreviewMaxMetadataBytes = 2 << 20
	geminiImagePreviewMaxKeyBytes      = 4 << 10
)

type geminiImageURLItem struct {
	URL string `json:"url"`
}

type geminiImageURLResponse struct {
	Created int64                `json:"created"`
	Data    []geminiImageURLItem `json:"data"`
}

type geminiImageResponseParser struct {
	context   *gin.Context
	reader    *bufio.Reader
	metadata  bytes.Buffer
	images    []*openai.MadImageCacheEntry
	input     *io.LimitedReader
	inputMax  int64
	outputMax int
}

type geminiQuotedBase64Reader struct {
	reader *bufio.Reader
	done   bool
}

func (reader *geminiQuotedBase64Reader) Read(buffer []byte) (int, error) {
	if reader.done {
		return 0, io.EOF
	}
	written := 0
	for written < len(buffer) {
		value, err := reader.reader.ReadByte()
		if err != nil {
			if written > 0 {
				return written, nil
			}
			return 0, err
		}
		if value == '"' {
			reader.done = true
			if written > 0 {
				return written, nil
			}
			return 0, io.EOF
		}
		if value == '\\' {
			escaped, readErr := reader.reader.ReadByte()
			if readErr != nil {
				return written, readErr
			}
			if escaped != '/' {
				return written, errors.New("escaped Gemini image base64 data is unsupported")
			}
			value = '/'
		}
		if value == '\r' || value == '\n' || value == ' ' || value == '\t' {
			continue
		}
		if !isGeminiBase64Byte(value) {
			return written, fmt.Errorf("invalid Gemini image base64 byte 0x%02x", value)
		}
		buffer[written] = value
		written++
	}
	return written, nil
}

func isGeminiBase64Byte(value byte) bool {
	return value >= 'A' && value <= 'Z' ||
		value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' ||
		value == '+' || value == '/' || value == '='
}

func (parser *geminiImageResponseParser) writeByte(value byte) error {
	if parser.metadata.Len() >= parser.outputMax {
		return errors.New("Gemini image response metadata is too large")
	}
	return parser.metadata.WriteByte(value)
}

func (parser *geminiImageResponseParser) write(values []byte) error {
	if len(values) > parser.outputMax-parser.metadata.Len() {
		return errors.New("Gemini image response metadata is too large")
	}
	_, err := parser.metadata.Write(values)
	return err
}

func (parser *geminiImageResponseParser) copyWhitespace() error {
	for {
		value, err := parser.reader.Peek(1)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch value[0] {
		case ' ', '\t', '\r', '\n':
			read, readErr := parser.reader.ReadByte()
			if readErr != nil {
				return readErr
			}
			if writeErr := parser.writeByte(read); writeErr != nil {
				return writeErr
			}
		default:
			return nil
		}
	}
}

func (parser *geminiImageResponseParser) copyString(capture bool) (string, error) {
	first, err := parser.reader.ReadByte()
	if err != nil {
		return "", err
	}
	if first != '"' {
		return "", errors.New("expected JSON string")
	}
	if err = parser.writeByte(first); err != nil {
		return "", err
	}
	var raw bytes.Buffer
	if capture {
		raw.WriteByte(first)
	}
	escaped := false
	for {
		value, readErr := parser.reader.ReadByte()
		if readErr != nil {
			return "", readErr
		}
		if value < 0x20 {
			return "", errors.New("invalid control byte in JSON string")
		}
		if err = parser.writeByte(value); err != nil {
			return "", err
		}
		if capture {
			if raw.Len() >= geminiImagePreviewMaxKeyBytes {
				return "", errors.New("Gemini image response JSON key is too large")
			}
			raw.WriteByte(value)
		}
		if escaped {
			escaped = false
			continue
		}
		if value == '\\' {
			escaped = true
			continue
		}
		if value == '"' {
			break
		}
	}
	if !capture {
		return "", nil
	}
	var decoded string
	if err = common.Unmarshal(raw.Bytes(), &decoded); err != nil {
		return "", err
	}
	return decoded, nil
}

func (parser *geminiImageResponseParser) parseInlineImage() error {
	if err := parser.copyWhitespace(); err != nil {
		return err
	}
	opening, err := parser.reader.ReadByte()
	if err != nil {
		return err
	}
	if opening != '"' {
		return errors.New("Gemini inlineData.data is not a JSON string")
	}
	if err = parser.write([]byte(`""`)); err != nil {
		return err
	}
	if len(parser.images) >= dto.MaxImageN {
		return fmt.Errorf("Gemini image response exceeds %d images", dto.MaxImageN)
	}
	encoded := &geminiQuotedBase64Reader{reader: parser.reader}
	decoder := base64.NewDecoder(base64.StdEncoding, encoded)
	entry, err := openai.CacheMadImageReader(parser.context, decoder)
	if err != nil {
		return fmt.Errorf("cache Gemini inline image: %w", err)
	}
	parser.images = append(parser.images, entry)
	return nil
}

func (parser *geminiImageResponseParser) parseObject(inlineData bool) error {
	opening, err := parser.reader.ReadByte()
	if err != nil {
		return err
	}
	if opening != '{' {
		return errors.New("expected JSON object")
	}
	if err = parser.writeByte(opening); err != nil {
		return err
	}
	if err = parser.copyWhitespace(); err != nil {
		return err
	}
	if value, peekErr := parser.reader.Peek(1); peekErr == nil && value[0] == '}' {
		closing, _ := parser.reader.ReadByte()
		return parser.writeByte(closing)
	}
	for {
		key, keyErr := parser.copyString(true)
		if keyErr != nil {
			return keyErr
		}
		if err = parser.copyWhitespace(); err != nil {
			return err
		}
		colon, readErr := parser.reader.ReadByte()
		if readErr != nil {
			return readErr
		}
		if colon != ':' {
			return errors.New("expected colon after JSON object key")
		}
		if err = parser.writeByte(colon); err != nil {
			return err
		}
		if inlineData && key == "data" {
			err = parser.parseInlineImage()
		} else {
			err = parser.parseValue(key == "inlineData" || key == "inline_data")
		}
		if err != nil {
			return err
		}
		if err = parser.copyWhitespace(); err != nil {
			return err
		}
		separator, readErr := parser.reader.ReadByte()
		if readErr != nil {
			return readErr
		}
		if err = parser.writeByte(separator); err != nil {
			return err
		}
		switch separator {
		case '}':
			return nil
		case ',':
			if err = parser.copyWhitespace(); err != nil {
				return err
			}
		default:
			return errors.New("expected comma or closing brace in JSON object")
		}
	}
}

func (parser *geminiImageResponseParser) parseArray() error {
	opening, err := parser.reader.ReadByte()
	if err != nil {
		return err
	}
	if opening != '[' {
		return errors.New("expected JSON array")
	}
	if err = parser.writeByte(opening); err != nil {
		return err
	}
	if err = parser.copyWhitespace(); err != nil {
		return err
	}
	if value, peekErr := parser.reader.Peek(1); peekErr == nil && value[0] == ']' {
		closing, _ := parser.reader.ReadByte()
		return parser.writeByte(closing)
	}
	for {
		if err = parser.parseValue(false); err != nil {
			return err
		}
		if err = parser.copyWhitespace(); err != nil {
			return err
		}
		separator, readErr := parser.reader.ReadByte()
		if readErr != nil {
			return readErr
		}
		if err = parser.writeByte(separator); err != nil {
			return err
		}
		switch separator {
		case ']':
			return nil
		case ',':
		default:
			return errors.New("expected comma or closing bracket in JSON array")
		}
	}
}

func (parser *geminiImageResponseParser) parsePrimitive() error {
	written := 0
	for {
		value, err := parser.reader.Peek(1)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		switch value[0] {
		case ' ', '\t', '\r', '\n', ',', ']', '}':
			if written == 0 {
				return errors.New("empty JSON primitive")
			}
			return nil
		}
		read, readErr := parser.reader.ReadByte()
		if readErr != nil {
			return readErr
		}
		if err = parser.writeByte(read); err != nil {
			return err
		}
		written++
	}
	if written == 0 {
		return errors.New("empty JSON primitive")
	}
	return nil
}

func (parser *geminiImageResponseParser) parseValue(inlineData bool) error {
	if err := parser.copyWhitespace(); err != nil {
		return err
	}
	value, err := parser.reader.Peek(1)
	if err != nil {
		return err
	}
	switch value[0] {
	case '{':
		return parser.parseObject(inlineData)
	case '[':
		return parser.parseArray()
	case '"':
		_, err = parser.copyString(false)
		return err
	default:
		return parser.parsePrimitive()
	}
}

func (parser *geminiImageResponseParser) parseDocument() error {
	if err := parser.parseValue(false); err != nil {
		return err
	}
	if err := parser.copyWhitespace(); err != nil {
		return err
	}
	if _, err := parser.reader.Peek(1); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("unexpected trailing data in Gemini image response")
	}
	return nil
}

func readGeminiImagePreviewResponse(c *gin.Context, body io.Reader) ([]byte, []*openai.MadImageCacheEntry, error) {
	limited := &io.LimitedReader{R: body, N: geminiImagePreviewMaxResponseBytes + 1}
	parser := &geminiImageResponseParser{
		context:   c,
		reader:    bufio.NewReaderSize(limited, 64<<10),
		input:     limited,
		inputMax:  geminiImagePreviewMaxResponseBytes,
		outputMax: geminiImagePreviewMaxMetadataBytes,
		images:    make([]*openai.MadImageCacheEntry, 0, 1),
	}
	err := parser.parseDocument()
	if limited.N == 0 {
		err = fmt.Errorf("Gemini image response exceeds %d bytes", parser.inputMax)
	}
	return append([]byte(nil), parser.metadata.Bytes()...), parser.images, err
}

func GeminiImagePreviewHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	metadata, images, err := readGeminiImagePreviewResponse(c, resp.Body)
	keepImages := false
	defer func() {
		if keepImages {
			return
		}
		for _, image := range images {
			image.Remove()
		}
	}()
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}

	var geminiResponse dto.GeminiChatResponse
	if err = common.Unmarshal(metadata, &geminiResponse); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	usage := buildUsageFromGeminiResponse(c, info, &geminiResponse)
	if len(images) == 0 {
		if geminiResponse.PromptFeedback != nil && geminiResponse.PromptFeedback.BlockReason != nil {
			common.SetContextKey(c, systemconstant.ContextKeyAdminRejectReason, fmt.Sprintf("gemini_block_reason=%s", *geminiResponse.PromptFeedback.BlockReason))
			apiError := types.NewOpenAIError(
				errors.New("request blocked by Gemini API: "+*geminiResponse.PromptFeedback.BlockReason),
				types.ErrorCodePromptBlocked,
				http.StatusBadRequest,
			)
			service.ResetStatusCode(apiError, c.GetString("status_code_mapping"))
			c.JSON(apiError.StatusCode, gin.H{"error": apiError.ToOpenAIError()})
			return &usage, nil
		}
		common.SetContextKey(c, systemconstant.ContextKeyAdminRejectReason, "gemini_empty_image_response")
		return &usage, types.NewOpenAIError(errors.New("Gemini response contained no generated image"), types.ErrorCodeEmptyResponse, http.StatusBadGateway)
	}
	if expected := geminiResponseInlineImageCount(&geminiResponse); expected != len(images) {
		return &usage, types.NewOpenAIError(
			fmt.Errorf("Gemini image response decoded %d image(s), metadata described %d", len(images), expected),
			types.ErrorCodeBadResponseBody,
			http.StatusBadGateway,
		)
	}

	clientResponse := geminiImageURLResponse{
		Created: common.GetTimestamp(),
		Data:    make([]geminiImageURLItem, 0, len(images)),
	}
	for _, image := range images {
		clientResponse.Data = append(clientResponse.Data, geminiImageURLItem{URL: image.URL})
	}
	encoded, err := common.Marshal(clientResponse)
	if err != nil {
		return &usage, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	service.IOCopyBytesGracefully(c, resp, encoded)
	keepImages = true
	return &usage, nil
}
