package common

import "github.com/gin-gonic/gin"

const nativeV1CodexContextKey = "madapi_native_v1_codex_client"

func MarkNativeV1CodexClient(c *gin.Context) {
	if c != nil {
		c.Set(nativeV1CodexContextKey, true)
	}
}

func IsNativeV1CodexClient(c *gin.Context) bool {
	return c != nil && c.GetBool(nativeV1CodexContextKey)
}
