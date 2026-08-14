package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

const playgroundRequestContextKey = "is_playground_request"

// NormalizePlaygroundRequestPath lets dashboard requests reuse the production
// /v1 routing and adapter logic without exposing or storing an API token.
func NormalizePlaygroundRequestPath() gin.HandlerFunc {
	return func(c *gin.Context) {
		originalPath := c.Request.URL.Path
		if !strings.HasPrefix(originalPath, "/pg/") {
			c.Next()
			return
		}

		originalRawPath := c.Request.URL.RawPath
		originalRequestURI := c.Request.RequestURI
		translatedPath := "/v1/" + strings.TrimPrefix(originalPath, "/pg/")

		c.Set(playgroundRequestContextKey, true)
		c.Request.URL.Path = translatedPath
		c.Request.URL.RawPath = ""
		c.Request.RequestURI = translatedPath
		if c.Request.URL.RawQuery != "" {
			c.Request.RequestURI += "?" + c.Request.URL.RawQuery
		}

		c.Next()

		c.Request.URL.Path = originalPath
		c.Request.URL.RawPath = originalRawPath
		c.Request.RequestURI = originalRequestURI
	}
}
