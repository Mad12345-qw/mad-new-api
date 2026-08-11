package middleware

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	cpaControlTokenEnv    = "MADAPI_CPA_CONTROL_TOKEN"
	cpaControlTokenHeader = "X-MadAPI-CPA-Control-Token"
	cpaRequestPathHeader  = "X-MadAPI-CPA-Request-Path"
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
