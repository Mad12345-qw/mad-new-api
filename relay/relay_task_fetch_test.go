package relay

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRelayTaskFetchRejectsUnknownMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())

	taskErr := RelayTaskFetch(context, -1)
	if taskErr == nil {
		t.Fatal("expected invalid relay mode error")
	}
	if taskErr.Code != "invalid_relay_mode" {
		t.Fatalf("unexpected error code: %s", taskErr.Code)
	}
}
