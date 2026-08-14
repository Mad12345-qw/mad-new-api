package middleware

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
)

const (
	cpaControlTokenEnv    = "MADAPI_CPA_CONTROL_TOKEN"
	cpaControlTokenHeader = "X-MadAPI-CPA-Control-Token"
	cpaRequestPathHeader  = "X-MadAPI-CPA-Request-Path"
	cpaLoginModeHeader    = "X-MadAPI-Codex-Login-Mode"
)

// CPAControlAuth limits the CPA control plane to the private gateway.
func CPAControlAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		expected := strings.TrimSpace(os.Getenv(cpaControlTokenEnv))
		provided := strings.TrimSpace(c.GetHeader(cpaControlTokenHeader))
		if len(expected) < 32 || len(expected) != len(provided) ||
			subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid CPA control credential"})
			return
		}
		c.Next()
	}
}

// CPAControlModelSlots restores API-mode Codex model shells before NewAPI
// applies token limits, channel selection, pricing, and billing. OAuth mode
// uses its native model names and never enters this mapping.
func CPAControlModelSlots() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.EqualFold(strings.TrimSpace(c.GetHeader(cpaLoginModeHeader)), "apikey") {
			c.Next()
			return
		}
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid CPA request body"})
			return
		}
	raw, err := storage.Bytes()
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid CPA request body"})
			return
		}
		var payload map[string]json.RawMessage
		if err = json.Unmarshal(raw, &payload); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid CPA request body"})
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
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid CPA request body"})
			return
		}
		common.CleanupBodyStorage(c)
		c.Set(common.KeyRequestBody, rewritten)
		c.Request.Body = io.NopCloser(bytes.NewReader(rewritten))
		c.Request.ContentLength = int64(len(rewritten))
		c.Next()
	}
}

// CPAControlRequestPath restores the public CPA path before NewAPI channel
// selection so capability checks and Advanced Custom routes see the real path.
func CPAControlRequestPath() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := strings.TrimSpace(c.GetHeader(cpaRequestPathHeader))
		switch path {
		case "/v1/responses", "/v1/responses/compact",
			"/v1/images/generations", "/v1/images/edits":
			c.Request.URL.Path = path
		default:
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "unsupported CPA request path"})
			return
		}
		c.Next()
	}
}
