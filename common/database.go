package common

import "strings"

type DatabaseType string

const (
	DatabaseTypeMySQL      DatabaseType = "mysql"
	DatabaseTypeSQLite     DatabaseType = "sqlite"
	DatabaseTypePostgreSQL DatabaseType = "postgres"
	DatabaseTypeClickHouse DatabaseType = "clickhouse"
)

var mainDatabaseType = DatabaseTypeSQLite
var logDatabaseType = DatabaseTypeSQLite

func MainDatabaseType() DatabaseType {
	return mainDatabaseType
}

func LogDatabaseType() DatabaseType {
	return logDatabaseType
}

func SetMainDatabaseType(databaseType DatabaseType) {
	mainDatabaseType = databaseType
}

func SetLogDatabaseType(databaseType DatabaseType) {
	logDatabaseType = databaseType
}

func SetDatabaseTypes(mainType DatabaseType, logType DatabaseType) {
	mainDatabaseType = mainType
	logDatabaseType = logType
}

func UsingMainDatabase(databaseType DatabaseType) bool {
	return mainDatabaseType == databaseType
}

func UsingLogDatabase(databaseType DatabaseType) bool {
	return logDatabaseType == databaseType
}

// withSQLitePragmas preserves an explicitly configured SQLite path while
// ensuring every modernc SQLite connection receives the production safety
// pragmas. This is also applied to SQLITE_PATH during process initialization.
func withSQLitePragmas(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	lowerPath := strings.ToLower(path)
	if !strings.Contains(lowerPath, "_pragma=busy_timeout") {
		path += separator + "_pragma=busy_timeout(30000)"
		separator = "&"
	}
	if !strings.Contains(lowerPath, "_pragma=journal_mode") {
		path += separator + "_pragma=journal_mode(WAL)"
	}
	return path
}

var SQLitePath = withSQLitePragmas("one-api.db")
