package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReserveTaskRequestIsIdempotentAndRejectsBodyDrift(t *testing.T) {
	truncateTables(t)

	first, created, err := ReserveTaskRequest(7, 11, "client-key", "body-a", "task_first")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "task_first", first.TaskID)

	replayed, created, err := ReserveTaskRequest(7, 11, "client-key", "body-a", "task_second")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, "task_first", replayed.TaskID)
	require.Equal(t, "body-a", replayed.RequestHash)

	differentToken, created, err := ReserveTaskRequest(7, 12, "client-key", "body-b", "task_other_token")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "task_other_token", differentToken.TaskID)
}

func TestUpdateTaskRequestReservationPersistsTerminalState(t *testing.T) {
	truncateTables(t)

	_, created, err := ReserveTaskRequest(3, 5, "recover-me", "request-hash", "task_recover")
	require.NoError(t, err)
	require.True(t, created)

	require.NoError(t, UpdateTaskRequestReservation(
		3,
		5,
		"recover-me",
		TaskRequestStatusFailed,
		"upstream_failed",
		"supplier rejected the task",
	))

	reservation, found, err := GetTaskRequestReservation(3, 5, "recover-me")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, TaskRequestStatusFailed, reservation.Status)
	require.Equal(t, "upstream_failed", reservation.ErrorCode)
	require.Equal(t, "supplier rejected the task", reservation.ErrorMessage)
}
