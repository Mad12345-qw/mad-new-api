package controller

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func retryError(status int, code types.ErrorCode, message string) *types.NewAPIError {
	return types.NewErrorWithStatusCode(errors.New(message), code, status)
}

func TestRetryablePrecommitErrors(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusGatewayTimeout, 524} {
		require.True(t, isRetryablePrecommitError(retryError(status, types.ErrorCodeBadResponseStatusCode, "upstream failed")))
	}
	require.True(t, isRetryablePrecommitError(retryError(
		http.StatusOK,
		types.ErrorCodeBadResponseBody,
		"malformed upstream response",
	)))
	require.True(t, isRetryablePrecommitError(retryError(
		http.StatusBadRequest,
		types.ErrorCodeBadResponseStatusCode,
		`json: unknown field "safetySettings"`,
	)))
}

func TestClientValidationErrorIsNotRetryablePrecommit(t *testing.T) {
	require.False(t, isRetryablePrecommitError(retryError(
		http.StatusBadRequest,
		types.ErrorCodeInvalidRequest,
		"prompt is required",
	)))
}
