package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newTrustedRouteTestContext(t *testing.T, token string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if token != "" {
		ctx.Request.Header.Set(trustedRouteTokenHeader, token)
	}
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	return ctx, recorder
}

func TestTrustedRouteGroupLeavesOrdinaryRequestsUnchanged(t *testing.T) {
	t.Setenv("TRUSTED_ROUTE_GROUP", "codex")
	t.Setenv("TRUSTED_ROUTE_TOKEN", "internal-secret")
	ctx, recorder := newTrustedRouteTestContext(t, "")

	TrustedRouteGroup()(ctx)

	require.False(t, ctx.IsAborted())
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "default", common.GetContextKeyString(ctx, constant.ContextKeyUsingGroup))
}

func TestTrustedRouteGroupPinsAuthenticatedIngress(t *testing.T) {
	t.Setenv("TRUSTED_ROUTE_GROUP", "codex")
	t.Setenv("TRUSTED_ROUTE_TOKEN", "internal-secret")
	ctx, recorder := newTrustedRouteTestContext(t, "internal-secret")

	TrustedRouteGroup()(ctx)

	require.False(t, ctx.IsAborted())
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "codex", common.GetContextKeyString(ctx, constant.ContextKeyUsingGroup))
	require.True(t, IsTrustedCodexRoute(ctx))
	require.Empty(t, ctx.Request.Header.Get(trustedRouteTokenHeader))
}

func TestTrustedRouteGroupCanPreserveAuthenticatedUserGroup(t *testing.T) {
	t.Setenv("TRUSTED_ROUTE_GROUP", "codex")
	t.Setenv("TRUSTED_ROUTE_TOKEN", "internal-secret")
	t.Setenv("TRUSTED_ROUTE_PRESERVE_USER_GROUP", "true")
	ctx, recorder := newTrustedRouteTestContext(t, "internal-secret")

	TrustedRouteGroup()(ctx)

	require.False(t, ctx.IsAborted())
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "default", common.GetContextKeyString(ctx, constant.ContextKeyUsingGroup))
	require.True(t, IsTrustedCodexRoute(ctx))
	require.Empty(t, ctx.Request.Header.Get(trustedRouteTokenHeader))
}

func TestTrustedRouteGroupRejectsInvalidIngressToken(t *testing.T) {
	t.Setenv("TRUSTED_ROUTE_GROUP", "codex")
	t.Setenv("TRUSTED_ROUTE_TOKEN", "internal-secret")
	ctx, recorder := newTrustedRouteTestContext(t, "wrong-secret")

	TrustedRouteGroup()(ctx)

	require.True(t, ctx.IsAborted())
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Equal(t, "default", common.GetContextKeyString(ctx, constant.ContextKeyUsingGroup))
	require.Empty(t, ctx.Request.Header.Get(trustedRouteTokenHeader))
}
