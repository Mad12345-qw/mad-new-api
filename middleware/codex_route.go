package middleware

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexRouteContextKey = "madapi_codex_route"

const (
	codexResponsesLiteHeader   = "X-OpenAI-Internal-Codex-Responses-Lite"
	codexResponsesLiteMetadata = "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite"
)

func MarkCodexRoute() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(codexRouteContextKey, true)
		if c.Request != nil && c.Request.URL != nil {
			if suffix, ok := strings.CutPrefix(c.Request.URL.Path, "/codex/v1/"); ok {
				c.Request.URL.Path = "/v1/" + suffix
			}
		}
		c.Next()
	}
}

// NormalizeCodexResponsesLite preserves CPA's native Lite handling only for
// requests that satisfy the upstream Lite contract. Stale client catalogs can
// otherwise advertise Lite while sending current-turn reasoning context.
func NormalizeCodexResponsesLite() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("MADAPI_CODEX_LITE_COMPAT_ENABLED")), "false") {
			c.Next()
			return
		}
		if !IsCodexRoute(c) || c.Request == nil || c.Request.URL == nil || c.Request.URL.Path != "/v1/responses" {
			c.Next()
			return
		}
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid Codex request body"})
			return
		}
		raw, err := storage.Bytes()
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid Codex request body"})
			return
		}
		metadata := gjson.GetBytes(raw, codexResponsesLiteMetadata)
		lite := strings.EqualFold(strings.TrimSpace(c.GetHeader(codexResponsesLiteHeader)), "true") ||
			metadata.Type == gjson.True || metadata.Type == gjson.String && strings.EqualFold(strings.TrimSpace(metadata.String()), "true")
		if !lite || strings.EqualFold(strings.TrimSpace(gjson.GetBytes(raw, "reasoning.context").String()), "all_turns") {
			c.Next()
			return
		}

		c.Request.Header.Del(codexResponsesLiteHeader)
		rewritten, err := sjson.DeleteBytes(raw, codexResponsesLiteMetadata)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid Codex request body"})
			return
		}
		common.CleanupBodyStorage(c)
		c.Set(common.KeyRequestBody, rewritten)
		c.Request.Body = io.NopCloser(bytes.NewReader(rewritten))
		c.Request.ContentLength = int64(len(rewritten))
		c.Next()
	}
}

func IsCodexRoute(c *gin.Context) bool {
	return c != nil && c.GetBool(codexRouteContextKey)
}
