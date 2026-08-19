package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetSelfReturnsServiceUnavailableWithoutClearingSessionOnDatabaseFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	t.Cleanup(func() { model.DB = previousDB })

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	model.DB = db

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", 123)
	GetSelf(context)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.JSONEq(t, `{"success":false,"message":"common.database_error"}`, recorder.Body.String())
}

func TestGetSelfReturnsUnauthorizedOnlyWhenUserDoesNotExist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	t.Cleanup(func() { model.DB = previousDB })

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	model.DB = db

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", 123)
	GetSelf(context)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
	require.JSONEq(t, `{"success":false,"message":"auth.not_logged_in"}`, recorder.Body.String())
}

func TestLoginReturnsServiceUnavailableOnDatabaseFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	previousPasswordLoginEnabled := common.PasswordLoginEnabled
	t.Cleanup(func() {
		model.DB = previousDB
		common.PasswordLoginEnabled = previousPasswordLoginEnabled
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	model.DB = db
	common.PasswordLoginEnabled = true

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", strings.NewReader(`{"username":"test-user","password":"test-password"}`))
	context.Request.Header.Set("Content-Type", "application/json")
	Login(context)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.JSONEq(t, `{"success":false,"message":"common.database_error"}`, recorder.Body.String())
}
