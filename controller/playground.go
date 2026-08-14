package controller

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func setupPlaygroundContext(c *gin.Context) *types.NewAPIError {
	useAccessToken := c.GetBool("use_access_token")
	if useAccessToken {
		return types.NewError(errors.New("暂不支持使用 access token"), types.ErrorCodeAccessDenied, types.ErrOptionWithSkipRetry())
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatOpenAI, nil, nil)
	if err != nil {
		return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	userId := c.GetInt("id")

	// Write user context to ensure acceptUnsetRatio is available
	userCache, err := model.GetUserCache(userId)
	if err != nil {
		return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}
	userCache.WriteContext(c)

	tempToken := &model.Token{
		UserId: userId,
		Name:   fmt.Sprintf("playground-%s", relayInfo.UsingGroup),
		Group:  relayInfo.UsingGroup,
	}
	_ = middleware.SetupContextForToken(c, tempToken)
	return nil
}

func respondPlaygroundSetupError(c *gin.Context, newAPIError *types.NewAPIError) bool {
	if newAPIError == nil {
		return false
	}
	c.JSON(newAPIError.StatusCode, gin.H{
		"error": newAPIError.ToOpenAIError(),
	})
	return true
}

func playgroundRelay(c *gin.Context, relayFormat types.RelayFormat) {
	if respondPlaygroundSetupError(c, setupPlaygroundContext(c)) {
		return
	}
	Relay(c, relayFormat)
}

func Playground(c *gin.Context) {
	playgroundRelay(c, types.RelayFormatOpenAI)
}

func PlaygroundResponses(c *gin.Context) {
	playgroundRelay(c, types.RelayFormatOpenAIResponses)
}

func PlaygroundImage(c *gin.Context) {
	playgroundRelay(c, types.RelayFormatOpenAIImage)
}

func PlaygroundAudio(c *gin.Context) {
	playgroundRelay(c, types.RelayFormatOpenAIAudio)
}

func PlaygroundTask(c *gin.Context) {
	if respondPlaygroundSetupError(c, setupPlaygroundContext(c)) {
		return
	}
	RelayTask(c)
}

func PlaygroundTaskFetch(c *gin.Context) {
	c.Set("relay_mode", relayconstant.RelayModeVideoFetchByID)
	if respondPlaygroundSetupError(c, setupPlaygroundContext(c)) {
		return
	}
	RelayTaskFetch(c)
}
