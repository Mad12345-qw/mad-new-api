package middleware

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
)

const trustedRouteTokenHeader = "X-New-Api-Route-Token"

// TrustedRouteGroup lets a private ingress pin requests to an isolated channel
// group while preserving the authenticated user's token, quota, and billing.
func TrustedRouteGroup() gin.HandlerFunc {
	group := strings.TrimSpace(os.Getenv("TRUSTED_ROUTE_GROUP"))
	expectedToken := strings.TrimSpace(os.Getenv("TRUSTED_ROUTE_TOKEN"))

	return func(c *gin.Context) {
		providedToken := strings.TrimSpace(c.GetHeader(trustedRouteTokenHeader))
		c.Request.Header.Del(trustedRouteTokenHeader)

		if providedToken == "" {
			c.Next()
			return
		}
		if group == "" || expectedToken == "" ||
			subtle.ConstantTimeCompare([]byte(providedToken), []byte(expectedToken)) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"message": "trusted route authentication failed",
					"type":    "access_denied",
					"code":    "trusted_route_auth_failed",
				},
			})
			return
		}

		common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
		c.Next()
	}
}
