package model

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	TaskRequestStatusReserved  = "reserved"
	TaskRequestStatusSubmitted = "submitted"
	TaskRequestStatusFailed    = "failed"
)

// TaskRequestReservation makes paid asynchronous task submission idempotent.
// The raw client key is never stored; ownership and the key hash form the
// unique database identity, while RequestHash detects accidental key reuse.
type TaskRequestReservation struct {
	ID           int64  `json:"id" gorm:"primary_key;AUTO_INCREMENT"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	UserID       int    `json:"user_id" gorm:"uniqueIndex:idx_task_request_owner_key,priority:1;index"`
	TokenID      int    `json:"token_id" gorm:"uniqueIndex:idx_task_request_owner_key,priority:2;index"`
	KeyHash      string `json:"-" gorm:"type:varchar(64);uniqueIndex:idx_task_request_owner_key,priority:3"`
	RequestHash  string `json:"-" gorm:"type:varchar(64)"`
	TaskID       string `json:"task_id" gorm:"type:varchar(191);index"`
	Status       string `json:"status" gorm:"type:varchar(20);index"`
	ErrorCode    string `json:"error_code,omitempty" gorm:"type:varchar(64)"`
	ErrorMessage string `json:"error_message,omitempty" gorm:"type:text"`
}

func HashTaskRequestValue(value string) string {
	return HashTaskRequestBytes([]byte(value))
}

func HashTaskRequestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func HashTaskRequestParts(parts ...[]byte) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write(part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func ReserveTaskRequest(userID, tokenID int, key, requestHash, taskID string) (*TaskRequestReservation, bool, error) {
	reservation := &TaskRequestReservation{
		UserID:      userID,
		TokenID:     tokenID,
		KeyHash:     HashTaskRequestValue(strings.TrimSpace(key)),
		RequestHash: requestHash,
		TaskID:      taskID,
		Status:      TaskRequestStatusReserved,
	}
	createErr := DB.Create(reservation).Error
	if createErr == nil {
		return reservation, true, nil
	}

	existing, found, findErr := GetTaskRequestReservation(userID, tokenID, key)
	if findErr != nil {
		return nil, false, findErr
	}
	if found {
		return existing, false, nil
	}
	return nil, false, createErr
}

func GetTaskRequestReservation(userID, tokenID int, key string) (*TaskRequestReservation, bool, error) {
	if strings.TrimSpace(key) == "" {
		return nil, false, nil
	}
	var reservation TaskRequestReservation
	err := DB.Where(
		"user_id = ? AND token_id = ? AND key_hash = ?",
		userID,
		tokenID,
		HashTaskRequestValue(strings.TrimSpace(key)),
	).First(&reservation).Error
	found, err := RecordExist(err)
	if err != nil {
		return nil, false, err
	}
	return &reservation, found, nil
}

func UpdateTaskRequestReservation(userID, tokenID int, key, status, errorCode, errorMessage string) error {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	return DB.Model(&TaskRequestReservation{}).
		Where(
			"user_id = ? AND token_id = ? AND key_hash = ?",
			userID,
			tokenID,
			HashTaskRequestValue(strings.TrimSpace(key)),
		).
		Updates(map[string]any{
			"status":        status,
			"error_code":    errorCode,
			"error_message": errorMessage,
		}).Error
}
