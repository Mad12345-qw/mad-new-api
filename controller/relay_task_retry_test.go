package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUnsupportedTaskPlatformRetriesBeforeUpstreamSubmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	taskErr := service.TaskErrorWrapperLocal(
		errors.New("channel does not support this video task platform: 48"),
		"unsupported_task_platform",
		http.StatusServiceUnavailable,
	)

	require.True(t, taskErr.LocalError)
	require.True(t, shouldRetryTaskRelay(c, 33, taskErr, 1))
	require.False(t, shouldRetryTaskRelay(c, 33, taskErr, 0))

	c.Set("specific_channel_id", "33")
	require.False(t, shouldRetryTaskRelay(c, 33, taskErr, 1))
}

func TestTaskReplayPayloadUsesPublicTaskState(t *testing.T) {
	task := &model.Task{
		TaskID:   "task_public",
		Status:   model.TaskStatusSuccess,
		Progress: "100%",
		Properties: model.Properties{
			OriginModelName: "seedance-2.0-720p",
		},
	}
	payload := taskReplayPayload(task.TaskID, task)
	require.Equal(t, "task_public", payload["id"])
	require.Equal(t, dto.VideoStatusCompleted, payload["status"])
	require.Equal(t, "seedance-2.0-720p", payload["model"])
}
