package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateLegacyTwoFATimestampsSQLite(t *testing.T) {
	truncateTables(t)
	user := User{Username: "legacy-twofa-user", Password: "password"}
	require.NoError(t, DB.Create(&user).Error)

	now := time.Now().Unix()
	future := now + 300
	require.NoError(t, DB.Exec(
		"INSERT INTO two_fas (user_id, secret, is_enabled, failed_attempts, locked_until, last_used_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		user.Id, "legacy-secret", true, 1, future, 0, now, now,
	).Error)

	require.NoError(t, migrateLegacyTwoFATimestampsSQLite())
	twoFA, err := GetTwoFAByUserId(user.Id)
	require.NoError(t, err)
	require.NotNil(t, twoFA)
	require.NotNil(t, twoFA.LockedUntil)
	assert.WithinDuration(t, time.Unix(future, 0), *twoFA.LockedUntil, time.Second)
	assert.Nil(t, twoFA.LastUsedAt)
	assert.WithinDuration(t, time.Unix(now, 0), twoFA.CreatedAt, time.Second)
}
