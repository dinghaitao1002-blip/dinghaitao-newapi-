package model

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

func TaskModelConcurrencyIdentity(task *Task) common.ModelConcurrencyLeaseIdentity {
	return common.ModelConcurrencyLeaseIdentity{ID: "task:" + strconv.FormatInt(task.ID, 10), Model: task.Properties.OriginModelName, Group: task.Group, UserID: task.UserId, Storage: common.ModelConcurrencyStorage(), Persistent: true}
}

func ImageTaskModelConcurrencyIdentity(task *ImageAsyncTask) common.ModelConcurrencyLeaseIdentity {
	return common.ModelConcurrencyLeaseIdentity{ID: "image:" + task.ID, Model: task.Model, Group: task.UsingGroup, UserID: task.UserID, Storage: common.ModelConcurrencyStorage(), Persistent: true}
}

func ReleaseTaskModelConcurrency(identity common.ModelConcurrencyLeaseIdentity) {
	if identity.Model == "" || identity.UserID <= 0 {
		return
	}
	lease, err := common.RestoreModelConcurrencyLease(identity)
	if err == nil {
		err = lease.Release(context.Background())
	}
	if err != nil {
		common.SysError("durable model concurrency release failed: " + err.Error())
	}
	if identity.Group != "" {
		global := identity
		global.Group = ""
		ReleaseTaskModelConcurrency(global)
	}
	if normalized := ratio_setting.RoutingMatchModelName(identity.Model); normalized != "" && normalized != identity.Model {
		identity.Model = normalized
		ReleaseTaskModelConcurrency(identity)
	}
}

func isTerminalConcurrencyTask(task *Task) bool {
	return task.Status == TaskStatusSuccess || task.Status == TaskStatusFailure || task.Progress == "100%"
}

func modelConcurrencyTaskActive(ctx context.Context, identity common.ModelConcurrencyLeaseIdentity, config common.ModelConcurrencyConfig) (bool, error) {
	if id, ok := strings.CutPrefix(identity.ID, "task:"); ok {
		var task Task
		err := DB.WithContext(ctx).Select("id", "user_id", "status", "progress", "properties", "group").Where("id = ?", id).Take(&task).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return err == nil && task.UserId == identity.UserID && concurrencyModelMatches(task.Properties.OriginModelName, task.Group, identity, config) && !isTerminalConcurrencyTask(&task), err
	}
	if id, ok := strings.CutPrefix(identity.ID, "image:"); ok {
		var task ImageAsyncTask
		err := DB.WithContext(ctx).Select("id", "user_id", "model", "status", "using_group").Where("id = ?", id).Take(&task).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return err == nil && task.UserID == identity.UserID && concurrencyModelMatches(task.Model, task.UsingGroup, identity, config) && (task.Status == ImageAsyncStatusQueued || task.Status == ImageAsyncStatusRunning), err
	}
	return false, errors.New("invalid durable model concurrency task")
}

func concurrencyModelMatches(raw, group string, identity common.ModelConcurrencyLeaseIdentity, config common.ModelConcurrencyConfig) bool {
	if rule, enabled := setting.ResolveModelConcurrencyRule(config, raw, group); enabled {
		return rule.Model == identity.Model && rule.Group == identity.Group
	}
	return (identity.Group == "" || identity.Group == group) && (raw == identity.Model || ratio_setting.RoutingMatchModelName(raw) == identity.Model)
}

// ReleaseFinishedModelConcurrencyTask closes the insert-to-transfer race: a
// worker can finalize the row before the request transfers ownership to it.
func ReleaseFinishedModelConcurrencyTask(ctx context.Context, identity common.ModelConcurrencyLeaseIdentity) error {
	active, err := modelConcurrencyTaskActive(ctx, identity, common.ModelConcurrencyConfig{})
	if err != nil || active {
		return err
	}
	lease, err := common.RestoreModelConcurrencyLease(identity)
	if err != nil {
		return err
	}
	return lease.Release(ctx)
}

// ReconcileModelConcurrencyTasks uses existing durable task identities; no new
// task columns or serializers are required. DB failures never imply completion.
func ReconcileModelConcurrencyTasks(ctx context.Context, config common.ModelConcurrencyConfig) error {
	leases, err := common.ListDurableModelConcurrencyLeases(ctx)
	if err != nil {
		return err
	}
	// Register the new rule bucket before retiring an old one. Rule edits and
	// Redis failures must never leave accepted work temporarily uncounted.
	if err := recoverConfiguredModelConcurrencyTasks(ctx, config); err != nil {
		return err
	}
	for _, identity := range leases {
		active, err := modelConcurrencyTaskActive(ctx, identity, config)
		if err != nil {
			return err
		}
		if !active {
			lease, err := common.RestoreModelConcurrencyLease(identity)
			if err != nil {
				return err
			}
			if err := lease.Release(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func recoverConfiguredModelConcurrencyTasks(ctx context.Context, config common.ModelConcurrencyConfig) error {
	enabled := false
	if config.Enabled {
		for _, rule := range config.Rules {
			if rule.Enabled {
				enabled = true
				break
			}
		}
	}
	if !enabled {
		return nil
	}
	var lastID int64
	for {
		var tasks []Task
		err := DB.WithContext(ctx).Select("id", "user_id", "status", "progress", "properties", "group").
			Where("id > ? AND (status IS NULL OR status NOT IN ?) AND (progress IS NULL OR progress <> ?)", lastID, []TaskStatus{TaskStatusSuccess, TaskStatusFailure}, "100%").Order("id").Limit(200).Find(&tasks).Error
		if err != nil {
			return err
		}
		for i := range tasks {
			task := &tasks[i]
			lastID = task.ID
			if rule, enabled := setting.ResolveModelConcurrencyRule(config, task.Properties.OriginModelName, task.Group); enabled && task.UserId > 0 {
				identity := TaskModelConcurrencyIdentity(task)
				identity.Model, identity.Group = rule.Model, rule.Group
				if err := recoverModelConcurrencyTask(ctx, identity); err != nil {
					return err
				}
			}
		}
		if len(tasks) < 200 {
			break
		}
	}
	lastImageID := ""
	for {
		var tasks []ImageAsyncTask
		err := DB.WithContext(ctx).Select("id", "user_id", "model", "status", "using_group").
			Where("id > ? AND status IN ?", lastImageID, []string{ImageAsyncStatusQueued, ImageAsyncStatusRunning}).Order("id").Limit(200).Find(&tasks).Error
		if err != nil {
			return err
		}
		for i := range tasks {
			task := &tasks[i]
			lastImageID = task.ID
			if rule, enabled := setting.ResolveModelConcurrencyRule(config, task.Model, task.UsingGroup); enabled && task.UserID > 0 {
				identity := ImageTaskModelConcurrencyIdentity(task)
				identity.Model, identity.Group = rule.Model, rule.Group
				if err := recoverModelConcurrencyTask(ctx, identity); err != nil {
					return err
				}
			}
		}
		if len(tasks) < 200 {
			break
		}
	}
	return nil
}

func recoverModelConcurrencyTask(ctx context.Context, identity common.ModelConcurrencyLeaseIdentity) error {
	lease, err := common.RecoverModelConcurrencyLease(ctx, identity)
	if err != nil {
		return err
	}
	// A poll/cancel/delete may have finalized the row after the scan. Checking
	// again prevents resurrecting a slot whose terminal hook already released it.
	active, err := modelConcurrencyTaskActive(ctx, identity, common.ModelConcurrencyConfig{})
	if err != nil {
		return err
	}
	if !active {
		return lease.Release(ctx)
	}
	return nil
}

// ImageConcurrencyGroup is the persisted admission group, independent of a
// worker's new channel selection after queueing or restart.
func ImageConcurrencyGroup(ctx context.Context, id string, userID int) (string, error) {
	var task ImageAsyncTask
	err := DB.WithContext(ctx).Select("using_group").Where("id = ? AND user_id = ?", id, userID).Take(&task).Error
	return task.UsingGroup, err
}

// MoveImageConcurrencyGroup records the destination before a worker transfers
// its provisional slot. Recovery can then reconstruct accepted work if a
// process exits between the database write and Redis transfer.
func MoveImageConcurrencyGroup(ctx context.Context, id, owner, group string) error {
	if owner == "" || len(group) > 64 {
		return errors.New("invalid image concurrency ownership or group")
	}
	result := DB.WithContext(ctx).Model(&ImageAsyncTask{}).
		Where("id = ? AND owner = ? AND status = ? AND lease_until > ?", id, owner, ImageAsyncStatusRunning, common.GetTimestamp()).
		Update("using_group", group)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		var current int64
		err := DB.WithContext(ctx).Model(&ImageAsyncTask{}).
			Where("id = ? AND owner = ? AND status = ? AND lease_until > ? AND using_group = ?", id, owner, ImageAsyncStatusRunning, common.GetTimestamp(), group).
			Count(&current).Error
		if err != nil {
			return err
		}
		if current == 1 {
			return nil
		}
		return errors.New("image concurrency ownership expired")
	}
	return nil
}
