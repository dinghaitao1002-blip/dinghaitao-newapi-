package model

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ImageAsyncStatusQueued    = "queued"
	ImageAsyncStatusRunning   = "running"
	ImageAsyncStatusSucceeded = "succeeded"
	ImageAsyncStatusFailed    = "failed"
	ImageAsyncBillingReserved = "reserved"
	ImageAsyncBillingSettled  = "settled"
	ImageAsyncBillingRefunded = "refunded"
)

var (
	ErrImageAsyncConflict          = errors.New("image async idempotency conflict")
	ErrImageAsyncCapacity          = errors.New("image async queue capacity exceeded")
	ErrImageAsyncLeaseLost         = errors.New("image async task lease lost")
	ErrImageAsyncQuotaInsufficient = errors.New("image async quota insufficient")
	ErrImageAsyncBillingFinalized  = errors.New("image async billing already finalized")
	ErrImageAsyncNotFound          = errors.New("image async task not found")
	imageAsyncIDPattern            = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
	imageAsyncDigestPattern        = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
)

// ImageAsyncTask contains only metadata. Request bodies, generated images and
// authorization credentials must never be placed in this table.
type ImageAsyncTask struct {
	ID                         string  `json:"id" gorm:"type:varchar(64);primaryKey"`
	UserID                     int     `json:"-" gorm:"index:idx_image_async_user_status,priority:1"`
	TokenID                    int     `json:"-" gorm:"uniqueIndex:idx_image_async_idempotency,priority:1;index"`
	Status                     string  `json:"status" gorm:"type:varchar(16);index:idx_image_async_status;index:idx_image_async_user_status,priority:2;index:idx_image_async_node_status,priority:2"`
	Operation                  string  `json:"-" gorm:"type:varchar(64)"`
	Model                      string  `json:"model" gorm:"type:varchar(255)"`
	ContentType                string  `json:"-" gorm:"type:varchar(255)"`
	HeadersJSON                string  `json:"-" gorm:"type:text"`
	ClientIP                   string  `json:"-" gorm:"type:varchar(64)"`
	UserGroup                  string  `json:"-" gorm:"type:varchar(64)"`
	TokenGroup                 string  `json:"-" gorm:"type:varchar(64)"`
	UsingGroup                 string  `json:"-" gorm:"type:varchar(64)"`
	SpecificChannelID          string  `json:"-" gorm:"type:varchar(32)"`
	RequestID                  string  `json:"request_id" gorm:"type:varchar(128)"`
	Fingerprint                string  `json:"-" gorm:"type:varchar(64)"`
	IdempotencyHash            *string `json:"-" gorm:"type:varchar(64);uniqueIndex:idx_image_async_idempotency,priority:2"`
	StorageNode                string  `json:"-" gorm:"type:varchar(128);index:idx_image_async_node_status,priority:1"`
	CreatedAt                  int64   `json:"created_at" gorm:"index"`
	StartedAt                  int64   `json:"started_at,omitempty"`
	FinishedAt                 int64   `json:"finished_at,omitempty"`
	ExpiresAt                  int64   `json:"expires_at" gorm:"index"`
	LeaseUntil                 int64   `json:"-" gorm:"index"`
	Owner                      string  `json:"-" gorm:"type:varchar(128)"`
	HTTPStatus                 int     `json:"-"`
	ErrorCode                  string  `json:"error_code,omitempty" gorm:"type:varchar(128)"`
	ErrorMessage               string  `json:"error_message,omitempty" gorm:"type:text"`
	BillingState               string  `json:"-" gorm:"type:varchar(16);index"`
	FundingSource              string  `json:"-" gorm:"type:varchar(16)"`
	BillingPreference          string  `json:"-" gorm:"type:varchar(32)"`
	ReservedQuota              int     `json:"-" gorm:"type:bigint;not null;default:0"`
	ActualQuota                int     `json:"-" gorm:"type:bigint;not null;default:0"`
	SubscriptionID             int     `json:"-"`
	SubscriptionAmountTotal    int64   `json:"-"`
	SubscriptionUsedBefore     int64   `json:"-"`
	SubscriptionUsedAfter      int64   `json:"-"`
	SubscriptionResetAt        int64   `json:"-"`
	SubscriptionPeriodReserved int64   `json:"-"`
}

// A short database write lock serializes admission and claims across processes.
// No network call or generation work runs while this lock is held.
type ImageAsyncQueueLock struct {
	ID         int   `gorm:"primaryKey;autoIncrement:false"`
	Generation int64 `gorm:"not null;default:0"`
}

func GenerateImageAsyncTaskID() (string, error) {
	id, err := common.GenerateRandomCharsKey(32)
	if err != nil {
		return "", err
	}
	return "imgtask_" + id, nil
}

func lockImageAsyncQueue(tx *gorm.DB) error {
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ImageAsyncQueueLock{ID: 1}).Error; err != nil {
		return err
	}
	return tx.Model(&ImageAsyncQueueLock{}).Where("id = ?", 1).UpdateColumn("generation", gorm.Expr("generation + 1")).Error
}

func validImageAsyncTask(task *ImageAsyncTask) error {
	if task == nil || !imageAsyncIDPattern.MatchString(task.ID) || task.UserID <= 0 || task.TokenID <= 0 || strings.TrimSpace(task.StorageNode) == "" || len(task.StorageNode) > 128 {
		return errors.New("invalid image async task identity")
	}
	if !imageAsyncDigestPattern.MatchString(task.Fingerprint) {
		return errors.New("invalid image async fingerprint")
	}
	if task.IdempotencyHash != nil && !imageAsyncDigestPattern.MatchString(*task.IdempotencyHash) {
		return errors.New("invalid image async idempotency hash")
	}
	if task.Operation != "/v1/images/generations" && task.Operation != "/v1/images/edits" && task.Operation != "/v1/edits" {
		return errors.New("invalid image async operation")
	}
	if len(task.HeadersJSON) > 16384 || len(task.ContentType) > 255 || len(task.Model) > 255 || len(task.RequestID) > 128 || len(task.UserGroup) > 64 || len(task.TokenGroup) > 64 || len(task.UsingGroup) > 64 || len(task.ClientIP) > 64 || len(task.SpecificChannelID) > 32 {
		return errors.New("image async metadata too large")
	}
	return nil
}

// CreateImageAsyncTask checks idempotency before capacity, so retries of an
// admitted request still succeed when the queue is full. The caller must have
// durably written its input before admitting the task.
func CreateImageAsyncTask(task *ImageAsyncTask, globalLimit, userLimit int) (*ImageAsyncTask, bool, error) {
	if err := validImageAsyncTask(task); err != nil {
		return nil, false, err
	}
	if globalLimit <= 0 || userLimit <= 0 {
		return nil, false, ErrImageAsyncCapacity
	}
	var result *ImageAsyncTask
	created := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockImageAsyncQueue(tx); err != nil {
			return err
		}
		if task.IdempotencyHash != nil {
			var existing ImageAsyncTask
			err := tx.Where("token_id = ? AND idempotency_hash = ?", task.TokenID, *task.IdempotencyHash).First(&existing).Error
			if err == nil {
				if existing.UserID != task.UserID || existing.Fingerprint != task.Fingerprint || existing.Operation != task.Operation {
					return ErrImageAsyncConflict
				}
				result = &existing
				return nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		statuses := []string{ImageAsyncStatusQueued, ImageAsyncStatusRunning}
		var global, user int64
		if err := tx.Model(&ImageAsyncTask{}).Where("status IN ?", statuses).Count(&global).Error; err != nil {
			return err
		}
		if global >= int64(globalLimit) {
			return ErrImageAsyncCapacity
		}
		if err := tx.Model(&ImageAsyncTask{}).Where("user_id = ? AND status IN ?", task.UserID, statuses).Count(&user).Error; err != nil {
			return err
		}
		if user >= int64(userLimit) {
			return ErrImageAsyncCapacity
		}
		copy := *task
		copy.Status = ImageAsyncStatusQueued
		copy.Owner, copy.BillingState, copy.FundingSource = "", "", ""
		copy.BillingPreference = ""
		copy.StartedAt, copy.FinishedAt, copy.LeaseUntil = 0, 0, 0
		copy.ReservedQuota, copy.ActualQuota, copy.SubscriptionID = 0, 0, 0
		copy.SubscriptionAmountTotal, copy.SubscriptionUsedBefore, copy.SubscriptionUsedAfter, copy.SubscriptionResetAt = 0, 0, 0, 0
		copy.SubscriptionPeriodReserved = 0
		copy.HTTPStatus, copy.ErrorCode, copy.ErrorMessage = 0, "", ""
		copy.CreatedAt = common.GetTimestamp()
		if copy.ExpiresAt <= copy.CreatedAt {
			return errors.New("image async task expiry must be in the future")
		}
		if err := tx.Create(&copy).Error; err != nil {
			return err
		}
		created, result = true, &copy
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return result, created, nil
}

func GetImageAsyncTask(id string, userID, tokenID int) (*ImageAsyncTask, error) {
	if userID <= 0 || tokenID <= 0 || !imageAsyncIDPattern.MatchString(id) {
		return nil, nil
	}
	var task ImageAsyncTask
	err := DB.Where("id = ? AND user_id = ? AND token_id = ?", id, userID, tokenID).First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// ImageAsyncTaskExists is for orphan-file cleanup, not public task lookup.
// Database errors must propagate so a transient outage cannot delete inputs.
func ImageAsyncTaskExists(id string) (bool, error) {
	if !imageAsyncIDPattern.MatchString(id) {
		return false, errors.New("invalid image async task ID")
	}
	var count int64
	err := DB.Model(&ImageAsyncTask{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

// Include all workers sharing this store, not only this process, when reserving
// worst-case result bytes before an upstream request starts.
func ImageAsyncRunningCount(node string) (int64, error) {
	var count int64
	err := DB.Model(&ImageAsyncTask{}).Where("storage_node = ? AND status = ?", node, ImageAsyncStatusRunning).Count(&count).Error
	return count, err
}

func ClaimImageAsyncTask(node, owner string, leaseSeconds int64) (*ImageAsyncTask, error) {
	if node == "" || owner == "" || len(owner) > 128 || leaseSeconds <= 0 || leaseSeconds > 86400 {
		return nil, errors.New("invalid image async lease")
	}
	var claimed *ImageAsyncTask
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockImageAsyncQueue(tx); err != nil {
			return err
		}
		now := common.GetTimestamp()
		var task ImageAsyncTask
		err := tx.Where("storage_node = ? AND status = ? AND expires_at > ?", node, ImageAsyncStatusQueued, now).Order("created_at ASC, id ASC").First(&task).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		r := tx.Model(&ImageAsyncTask{}).Where("id = ? AND status = ?", task.ID, ImageAsyncStatusQueued).Updates(map[string]any{"status": ImageAsyncStatusRunning, "owner": owner, "started_at": now, "lease_until": now + leaseSeconds})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrImageAsyncLeaseLost
		}
		task.Status, task.Owner, task.StartedAt, task.LeaseUntil = ImageAsyncStatusRunning, owner, now, now+leaseSeconds
		claimed = &task
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func RenewImageAsyncTaskLease(id, owner string, leaseSeconds int64) error {
	if owner == "" || leaseSeconds <= 0 || leaseSeconds > 86400 {
		return ErrImageAsyncLeaseLost
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		task, err := getImageAsyncTaskLocked(tx, id)
		if err != nil {
			return err
		}
		if !imageAsyncLeaseValid(task, owner) {
			return ErrImageAsyncLeaseLost
		}
		until := common.GetTimestamp() + leaseSeconds
		// A repeated renewal in the same second is valid. MySQL reports zero
		// changed rows for identical values, which must not be a lost lease.
		if until <= task.LeaseUntil {
			return nil
		}
		return tx.Model(task).UpdateColumn("lease_until", until).Error
	})
}

func getImageAsyncTaskLocked(tx *gorm.DB, id string) (*ImageAsyncTask, error) {
	var task ImageAsyncTask
	if err := lockForUpdate(tx).Where("id = ?", id).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrImageAsyncNotFound
		}
		return nil, err
	}
	return &task, nil
}

func imageAsyncLeaseValid(task *ImageAsyncTask, owner string) bool {
	return owner != "" && task.Status == ImageAsyncStatusRunning && task.Owner == owner && task.LeaseUntil > common.GetTimestamp()
}

func CompleteImageAsyncTask(id, owner string, httpStatus int, errorCode, msg string) error {
	if httpStatus < 200 || httpStatus > 599 {
		return errors.New("invalid image async result status")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		task, err := getImageAsyncTaskLocked(tx, id)
		if err != nil {
			return err
		}
		status := ImageAsyncStatusFailed
		if httpStatus < 300 {
			status = ImageAsyncStatusSucceeded
		}
		if task.Status == status && task.Owner == owner && task.HTTPStatus == httpStatus && task.ErrorCode == errorCode {
			return nil
		}
		if !imageAsyncLeaseValid(task, owner) {
			return ErrImageAsyncLeaseLost
		}
		if status == ImageAsyncStatusSucceeded && task.BillingState != ImageAsyncBillingSettled {
			return ErrImageAsyncBillingFinalized
		}
		// A failed/ambiguous commit acknowledgement can reach the worker after
		// settlement has actually committed. Keep that task recoverable rather
		// than permanently failing an already paid, durable image. Recovery
		// checks the artifact and decides its terminal state after lease expiry.
		// Explicit violation-fee failures are charged without an image artifact.
		if status == ImageAsyncStatusFailed && task.BillingState == ImageAsyncBillingSettled && !strings.HasPrefix(errorCode, "violation_fee.") {
			return ErrImageAsyncBillingFinalized
		}
		return tx.Model(task).Updates(map[string]any{"status": status, "http_status": httpStatus, "error_code": imageAsyncTrim(errorCode, 128), "error_message": imageAsyncTrim(msg, 4096), "finished_at": common.GetTimestamp(), "lease_until": 0}).Error
	})
}

func imageAsyncTrim(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}

func ListExpiredRunningImageTasks(node string) ([]*ImageAsyncTask, error) {
	var tasks []*ImageAsyncTask
	err := DB.Where("storage_node = ? AND status = ? AND lease_until <= ?", node, ImageAsyncStatusRunning, common.GetTimestamp()).Order("lease_until ASC, id ASC").Limit(100).Find(&tasks).Error
	return tasks, err
}

func ListImageAsyncRefundPending(node string, limit int) ([]*ImageAsyncTask, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var tasks []*ImageAsyncTask
	err := DB.Where("storage_node = ? AND status = ? AND billing_state = ?", node, ImageAsyncStatusFailed, ImageAsyncBillingReserved).Order("finished_at ASC, id ASC").Limit(limit).Find(&tasks).Error
	return tasks, err
}

// RecoverExpiredImageAsyncTask never retries generation. A completed artifact
// may only be recovered as success after its durable billing has settled.
func RecoverExpiredImageAsyncTask(id, node string, success bool, code, msg string) (bool, error) {
	changed := false
	var userID int
	var tokenKey string
	err := DB.Transaction(func(tx *gorm.DB) error {
		task, err := getImageAsyncTaskLocked(tx, id)
		if err != nil {
			return err
		}
		if task.StorageNode != node || task.Status != ImageAsyncStatusRunning || task.LeaseUntil > common.GetTimestamp() {
			return nil
		}
		status, httpStatus := ImageAsyncStatusFailed, 503
		if success {
			if task.BillingState != ImageAsyncBillingSettled {
				return ErrImageAsyncBillingFinalized
			}
			status, httpStatus, code, msg = ImageAsyncStatusSucceeded, 200, "", ""
		}
		task.Status = status
		if !success && task.BillingState == ImageAsyncBillingReserved {
			key, err := refundImageAsyncQuotaTx(tx, task)
			if err != nil {
				return err
			}
			userID, tokenKey = task.UserID, key
		}
		if err := tx.Model(task).Updates(map[string]any{"status": status, "http_status": httpStatus, "error_code": imageAsyncTrim(code, 128), "error_message": imageAsyncTrim(msg, 4096), "finished_at": common.GetTimestamp(), "lease_until": 0}).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err == nil && userID > 0 {
		invalidateImageAsyncBillingCaches(userID, tokenKey)
	}
	if err != nil {
		return false, err
	}
	return changed, nil
}

func FailExpiredImageAsyncTask(id, node, errorCode, msg string) (bool, error) {
	return RecoverExpiredImageAsyncTask(id, node, false, errorCode, msg)
}

func ExpireQueuedImageAsyncTasks(node string) (int64, error) {
	now := common.GetTimestamp()
	r := DB.Model(&ImageAsyncTask{}).Where("storage_node = ? AND status = ? AND expires_at <= ? AND billing_state = ?", node, ImageAsyncStatusQueued, now, "").Updates(map[string]any{"status": ImageAsyncStatusFailed, "http_status": 410, "error_code": "image_task_expired", "finished_at": now})
	return r.RowsAffected, r.Error
}

func ListExpiredImageAsyncTasks(node string, limit int) ([]*ImageAsyncTask, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var tasks []*ImageAsyncTask
	err := DB.Where("storage_node = ? AND status IN ? AND expires_at <= ? AND billing_state <> ?", node, []string{ImageAsyncStatusSucceeded, ImageAsyncStatusFailed}, common.GetTimestamp(), ImageAsyncBillingReserved).Order("expires_at ASC, id ASC").Limit(limit).Find(&tasks).Error
	return tasks, err
}

func DeleteExpiredImageAsyncTask(id, node string) error {
	r := DB.Where("id = ? AND storage_node = ? AND status IN ? AND expires_at <= ? AND billing_state <> ?", id, node, []string{ImageAsyncStatusSucceeded, ImageAsyncStatusFailed}, common.GetTimestamp(), ImageAsyncBillingReserved).Delete(&ImageAsyncTask{})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return fmt.Errorf("image async task is not eligible for deletion")
	}
	return nil
}
