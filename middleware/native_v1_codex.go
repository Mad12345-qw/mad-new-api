package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

const nativeV1CodexActorHeader = "X-OpenAI-Actor-Authorization"
const nativeV1CodexActor = "madapi-gateway"
const codexCockpitHeader = "X-MadAPI-Codex-Cockpit"

// NativeV1CodexClient enables the native Codex compatibility contract after a
// public /codex route has been normalized to its standard /v1 relay path.
func NativeV1CodexClient() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil || c.Request.URL == nil ||
			(c.Request.URL.Path != "/v1/responses" && c.Request.URL.Path != "/v1/responses/compact") ||
			!IsCodexRoute(c) {
			c.Next()
			return
		}

		loginMode := strings.ToLower(strings.TrimSpace(c.GetHeader(cpaLoginModeHeader)))
		if loginMode != "oauth" && loginMode != "apikey" {
			if strings.TrimSpace(c.GetHeader(codexCockpitHeader)) == "1" {
				loginMode = "apikey"
			} else {
				loginMode = "oauth"
			}
		}
		relaycommon.MarkNativeV1CodexClient(c)
		if loginMode != "apikey" {
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
		var payload map[string]json.RawMessage
		if err = json.Unmarshal(raw, &payload); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid Codex request body"})
			return
		}
		var shell string
		if err = json.Unmarshal(payload["model"], &shell); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "model is required"})
			return
		}
		target, ok := constant.CodexAPIModelSlots[strings.ToLower(strings.TrimSpace(shell))]
		if !ok || strings.EqualFold(shell, target) {
			c.Next()
			return
		}
		payload["model"], _ = json.Marshal(target)
		rewritten, err := json.Marshal(payload)
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
