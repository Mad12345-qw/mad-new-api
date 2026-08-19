package model

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSQLiteConnectionDefaultsUseWALAndBusyTimeout(t *testing.T) {
	dsnSuffix := strings.TrimPrefix(common.SQLitePath, "one-api.db")
	dsn := filepath.Join(t.TempDir(), "pool-test.db") + dsnSuffix
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)

	var journalMode string
	require.NoError(t, db.Raw("PRAGMA journal_mode").Scan(&journalMode).Error)
	require.Equal(t, "wal", strings.ToLower(journalMode))

	var busyTimeout int
	require.NoError(t, db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error)
	require.Equal(t, 30000, busyTimeout)
}

func TestConfigureSQLConnectionPoolBoundsSQLite(t *testing.T) {
	t.Setenv("SQL_MAX_IDLE_CONNS", "")
	t.Setenv("SQL_MAX_OPEN_CONNS", "")
	t.Setenv("SQL_MAX_LIFETIME", "")
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	configureSQLConnectionPool(db, common.DatabaseTypeSQLite)
	require.Equal(t, 16, db.Stats().MaxOpenConnections)
}
