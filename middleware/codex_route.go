package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

const codexRouteContextKey = "madapi_codex_route"

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

func IsCodexRoute(c *gin.Context) bool {
	return c != nil && c.GetBool(codexRouteContextKey)
}
