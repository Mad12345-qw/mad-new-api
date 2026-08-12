package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func TestCPAControlModelSlotsMapsAPIKeyGrok46(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(BodyStorageCleanup())
	engine.Use(CPAControlModelSlots())
	engine.POST("/dispatch", func(c *gin.Context) {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			c.String(http.StatusBadRequest, err.Error())
			return
		}
		body, err := storage.Bytes()
		if err != nil {
			c.String(http.StatusBadRequest, err.Error())
			return
		}
		var payload map[string]any
		if err = json.Unmarshal(body, &payload); err != nil {
			c.String(http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, payload)
	})

	request := httptest.NewRequest(
		http.MethodPost,
		"/dispatch",
		strings.NewReader(`{"model":"gpt-5.4-mini","input":"hello"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(cpaLoginModeHeader, "apikey")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["model"] != "grok-4.6" {
		t.Fatalf("model = %v, want grok-4.6", payload["model"])
	}
}

func TestCPAControlModelSlotsLeavesOAuthGrokShellUntouched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(BodyStorageCleanup())
	engine.Use(CPAControlModelSlots())
	engine.POST("/dispatch", func(c *gin.Context) {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			c.String(http.StatusBadRequest, err.Error())
			return
		}
		body, err := storage.Bytes()
		if err != nil {
			c.String(http.StatusBadRequest, err.Error())
			return
		}
		c.Data(http.StatusOK, "application/json", body)
	})

	request := httptest.NewRequest(
		http.MethodPost,
		"/dispatch",
		strings.NewReader(`{"model":"gpt-5.4-mini","input":"hello"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(cpaLoginModeHeader, "oauth")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["model"] != "gpt-5.4-mini" {
		t.Fatalf("OAuth model changed to %v", payload["model"])
	}
}
