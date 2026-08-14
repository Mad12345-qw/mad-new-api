package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestNormalizePlaygroundRequestPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(NormalizePlaygroundRequestPath())
	router.POST("/pg/images/generations", func(c *gin.Context) {
		require.Equal(t, "/v1/images/generations", c.Request.URL.Path)
		require.Equal(t, "/v1/images/generations?preview=true", c.Request.RequestURI)
		require.True(t, c.GetBool(playgroundRequestContextKey))
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/pg/images/generations?preview=true", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code)
}
