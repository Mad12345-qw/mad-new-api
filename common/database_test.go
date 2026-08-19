package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithSQLitePragmasAddsSafetySettingsToExplicitPath(t *testing.T) {
	path := withSQLitePragmas("/data/one-api.db?_busy_timeout=30000")
	require.Contains(t, path, "_busy_timeout=30000")
	require.Contains(t, path, "_pragma=busy_timeout(30000)")
	require.Contains(t, path, "_pragma=journal_mode(WAL)")
}

func TestWithSQLitePragmasDoesNotDuplicateExistingSettings(t *testing.T) {
	original := "/data/one-api.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	path := withSQLitePragmas(original)
	require.Equal(t, original, path)
	require.Equal(t, 1, strings.Count(strings.ToLower(path), "_pragma=busy_timeout"))
	require.Equal(t, 1, strings.Count(strings.ToLower(path), "_pragma=journal_mode"))
}
