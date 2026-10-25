package model

import (
	"errors"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// ImageAsyncBilling is a safe snapshot for the relay's BillingSettler adapter.
// Changed is false on an idempotent replay and is not persisted.
type ImageAsyncBilling struct {
	BillingState            string
	FundingSource           string
	ReservedQuota           int
	ActualQuota             int
	SubscriptionID          int
	SubscriptionAmountTotal int64
	SubscriptionUsedBefore  int64
	SubscriptionUsedAfter   int64
	Changed                 bool
}

func imageAsyncBillingSnapshot(task *ImageAsyncTask, changed bool) *ImageAsyncBilling {
	return &ImageAsyncBilling{
		BillingState: task.BillingState, FundingSource: task.FundingSource,
		ReservedQuota: task.ReservedQuota, ActualQuota: task.ActualQuota,
		SubscriptionID: task.SubscriptionID, SubscriptionAmountTotal: task.SubscriptionAmountTotal,
		SubscriptionUsedBefore: task.SubscriptionUsedBefore, SubscriptionUsedAfter: task.SubscriptionUsedAfter,
		Changed: changed,
	}
}

// Never call the public quota helpers inside these transactions: those helpers
// may batch writes or update Redis before the database transaction commits.
func invalidateImageAsyncBillingCaches(userID int, tokenKey string) {
	if !common.RedisEnabled {
		return
	}
	if err := invalidateUserCache(userID); err != nil {
		common.SysLog("image async billing: user cache invalidation failed")
	}
	if tokenKey != "" {
		if err := invalidateTokenCacheForMutation(tokenKey); err != nil {
			common.SysLog("image async billing: token cache invalidation failed")
		}
	}
}

func imageAsyncBillingAccounts(tx *gorm.DB, task *ImageAsyncTask, requireEnabled bool) (*User, *Token, error) {
	var user User
	if err := lockForUpdate(tx.Unscoped()).Where("id = ?", task.UserID).First(&user).Error; err != nil {
		return nil, nil, err
	}
	var token Token
	if err := lockForUpdate(tx.Unscoped()).Where("id = ? AND user_id = ?", task.TokenID, task.UserID).First(&token).Error; err != nil {
		return nil, nil, err
	}
	if requireEnabled {
		if user.Status != common.UserStatusEnabled || user.DeletedAt.Valid || token.Status != common.TokenStatusEnabled || token.DeletedAt.Valid || (token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp()) {
			return nil, nil, fmt.Errorf("%w: account or token unavailable", ErrImageAsyncQuotaInsufficient)
		}
	}
	return &user, &token, nil
}

func imageAsyncAdjustToken(tx *gorm.DB, token *Token, delta int) error {
	if delta == 0 {
		return nil
	}
	if delta > 0 && !token.UnlimitedQuota && token.RemainQuota < delta {
		return fmt.Errorf("%w: token quota", ErrImageAsyncQuotaInsufficient)
	}
	if (delta > 0 && (int64(token.UsedQuota) > math.MaxInt64-int64(delta) || int64(token.RemainQuota) < math.MinInt64+int64(delta))) ||
		(delta < 0 && (int64(token.RemainQuota) > math.MaxInt64+int64(delta) || int64(token.UsedQuota) < math.MinInt64-int64(delta))) {
		return errors.New("image async token quota arithmetic overflow")
	}
	// Unlimited tokens still accumulate UsedQuota and adjust RemainQuota, as
	// existing DecreaseTokenQuota/IncreaseTokenQuota do; only the limit is skipped.
	return tx.Unscoped().Model(&Token{}).Where("id = ? AND user_id = ?", token.Id, token.UserId).Updates(map[string]any{
		"remain_quota":  gorm.Expr("remain_quota - ?", delta),
		"used_quota":    gorm.Expr("used_quota + ?", delta),
		"accessed_time": common.GetTimestamp(),
	}).Error
}

func imageAsyncAdjustWallet(tx *gorm.DB, user *User, delta int) error {
	if delta == 0 {
		return nil
	}
	if delta > 0 && user.Quota < delta {
		return fmt.Errorf("%w: wallet quota", ErrImageAsyncQuotaInsufficient)
	}
	if delta < 0 && int64(user.Quota) > math.MaxInt64+int64(delta) {
		return errors.New("image async wallet quota arithmetic overflow")
	}
	return tx.Unscoped().Model(&User{}).Where("id = ?", user.Id).UpdateColumn("quota", gorm.Expr("quota - ?", delta)).Error
}

// chooseImageAsyncFunding keeps the existing wallet/subscription preference and
// overflow policy, including selecting the earliest expiring suitable plan.
func chooseImageAsyncFunding(tx *gorm.DB, task *ImageAsyncTask, user *User, amount int, preference string) error {
	if amount == 0 {
		// Free requests establish a durable zero ledger without consuming the
		// subscription path's historical minimum of one quota unit.
		task.FundingSource = "wallet"
		return nil
	}
	var subs []UserSubscription
	loaded := false
	loadSubscriptions := func() error {
		if loaded {
			return nil
		}
		if err := lockForUpdate(tx).Where("user_id = ? AND status = ? AND end_time > ?", task.UserID, "active", common.GetTimestamp()).Order("end_time ASC, id ASC").Find(&subs).Error; err != nil {
			return err
		}
		loaded = true
		return nil
	}
	tryWallet := func() error {
		if err := imageAsyncAdjustWallet(tx, user, amount); err != nil {
			return err
		}
		task.FundingSource = "wallet"
		return nil
	}
	trySubscription := func() error {
		if err := loadSubscriptions(); err != nil {
			return err
		}
		for _, candidate := range subs {
			sub := candidate
			plan, err := getSubscriptionPlanByIdTx(tx, sub.PlanId)
			if err != nil {
				return err
			}
			if err := maybeResetUserSubscriptionWithPlanTx(tx, &sub, plan, common.GetTimestamp()); err != nil {
				return err
			}
			if sub.AmountTotal > 0 && sub.AmountTotal-sub.AmountUsed < int64(amount) {
				continue
			}
			if sub.AmountUsed < 0 || sub.AmountUsed > math.MaxInt64-int64(amount) {
				return errors.New("image async subscription quota arithmetic overflow")
			}
			task.FundingSource, task.SubscriptionID = "subscription", sub.Id
			task.SubscriptionAmountTotal, task.SubscriptionUsedBefore = sub.AmountTotal, sub.AmountUsed
			task.SubscriptionUsedAfter, task.SubscriptionResetAt = sub.AmountUsed+int64(amount), sub.LastResetTime
			task.SubscriptionPeriodReserved = int64(amount)
			return tx.Model(&UserSubscription{}).Where("id = ?", sub.Id).UpdateColumn("amount_used", task.SubscriptionUsedAfter).Error
		}
		return fmt.Errorf("%w: no active subscription with sufficient quota", ErrImageAsyncQuotaInsufficient)
	}
	switch common.NormalizeBillingPreference(preference) {
	case "wallet_only":
		return tryWallet()
	case "subscription_only":
		return trySubscription()
	case "wallet_first":
		err := tryWallet()
		if errors.Is(err, ErrImageAsyncQuotaInsufficient) {
			return trySubscription()
		}
		return err
	default: // subscription_first
		if err := loadSubscriptions(); err != nil {
			return err
		}
		if len(subs) == 0 {
			return tryWallet()
		}
		err := trySubscription()
		if !errors.Is(err, ErrImageAsyncQuotaInsufficient) {
			return err
		}
		for _, sub := range subs {
			if !sub.AllowWalletOverflow {
				return err
			}
		}
		return tryWallet()
	}
}

func imageAsyncAdjustFunding(tx *gorm.DB, task *ImageAsyncTask, user *User, delta int) error {
	if delta == 0 {
		return nil
	}
	if task.FundingSource == "wallet" {
		return imageAsyncAdjustWallet(tx, user, delta)
	}
	if task.FundingSource != "subscription" || task.SubscriptionID <= 0 {
		return errors.New("invalid image async funding source")
	}
	var sub UserSubscription
	if err := lockForUpdate(tx).Where("id = ? AND user_id = ?", task.SubscriptionID, task.UserID).First(&sub).Error; err != nil {
		return err
	}
	// Only the portion reserved in the current subscription period is
	// refundable. A task can span a reset and then receive a supplemental
	// reservation; tracking that portion avoids both stealing quota from a
	// later period and stranding the supplement when the task fails.
	if sub.LastResetTime != task.SubscriptionResetAt {
		task.SubscriptionPeriodReserved = 0
		task.SubscriptionResetAt = sub.LastResetTime
	}
	if delta < 0 && int64(-delta) > task.SubscriptionPeriodReserved {
		delta = -int(task.SubscriptionPeriodReserved)
	}
	if delta > 0 && sub.AmountUsed > math.MaxInt64-int64(delta) {
		return errors.New("image async subscription quota arithmetic overflow")
	}
	next := sub.AmountUsed + int64(delta)
	if next < 0 {
		next = 0
	}
	if delta > 0 && sub.AmountTotal > 0 && next > sub.AmountTotal {
		return fmt.Errorf("%w: subscription quota", ErrImageAsyncQuotaInsufficient)
	}
	task.SubscriptionUsedAfter = next
	task.SubscriptionPeriodReserved += int64(delta)
	return tx.Model(&UserSubscription{}).Where("id = ?", sub.Id).UpdateColumn("amount_used", next).Error
}

func saveImageAsyncBilling(tx *gorm.DB, task *ImageAsyncTask) error {
	return tx.Model(task).Updates(map[string]any{
		"billing_state": task.BillingState, "funding_source": task.FundingSource,
		"billing_preference": task.BillingPreference,
		"reserved_quota":     task.ReservedQuota, "actual_quota": task.ActualQuota,
		"subscription_id": task.SubscriptionID, "subscription_amount_total": task.SubscriptionAmountTotal,
		"subscription_used_before": task.SubscriptionUsedBefore, "subscription_used_after": task.SubscriptionUsedAfter,
		"subscription_reset_at":        task.SubscriptionResetAt,
		"subscription_period_reserved": task.SubscriptionPeriodReserved,
	}).Error
}

func ReserveImageAsyncQuota(id, owner string, targetQuota int, preference string) (*ImageAsyncBilling, error) {
	if targetQuota < 0 || targetQuota > common.MaxQuota {
		return nil, errors.New("image async reserve quota is out of range")
	}
	var result *ImageAsyncBilling
	var userID int
	var tokenKey string
	err := DB.Transaction(func(tx *gorm.DB) error {
		task, err := getImageAsyncTaskLocked(tx, id)
		if err != nil {
			return err
		}
		if !imageAsyncLeaseValid(task, owner) {
			return ErrImageAsyncLeaseLost
		}
		if task.BillingState == ImageAsyncBillingSettled || task.BillingState == ImageAsyncBillingRefunded {
			return ErrImageAsyncBillingFinalized
		}
		if task.BillingState == ImageAsyncBillingReserved && targetQuota <= task.ReservedQuota {
			result = imageAsyncBillingSnapshot(task, false)
			return nil
		}
		user, token, err := imageAsyncBillingAccounts(tx, task, true)
		if err != nil {
			return err
		}
		delta := targetQuota - task.ReservedQuota
		if err := imageAsyncAdjustToken(tx, token, delta); err != nil {
			return err
		}
		if task.BillingState == "" {
			task.BillingPreference = common.NormalizeBillingPreference(preference)
		}
		if task.BillingState == "" || (task.ReservedQuota == 0 && task.SubscriptionID == 0) {
			if err := chooseImageAsyncFunding(tx, task, user, targetQuota, task.BillingPreference); err != nil {
				return err
			}
		} else if err := imageAsyncAdjustFunding(tx, task, user, delta); err != nil {
			return err
		}
		task.BillingState, task.ReservedQuota = ImageAsyncBillingReserved, targetQuota
		if err := saveImageAsyncBilling(tx, task); err != nil {
			return err
		}
		userID, tokenKey = task.UserID, token.Key
		result = imageAsyncBillingSnapshot(task, true)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if userID > 0 {
		invalidateImageAsyncBillingCaches(userID, tokenKey)
	}
	return result, nil
}

func SettleImageAsyncQuota(id, owner string, actualQuota int) (*ImageAsyncBilling, error) {
	if actualQuota < 0 || actualQuota > common.MaxQuota {
		return nil, errors.New("image async actual quota is out of range")
	}
	var result *ImageAsyncBilling
	var userID int
	var tokenKey string
	err := DB.Transaction(func(tx *gorm.DB) error {
		task, err := getImageAsyncTaskLocked(tx, id)
		if err != nil {
			return err
		}
		if task.BillingState == ImageAsyncBillingSettled {
			if owner == "" || task.Owner != owner || (task.Status != ImageAsyncStatusSucceeded && !imageAsyncLeaseValid(task, owner)) {
				return ErrImageAsyncLeaseLost
			}
			if task.ActualQuota != actualQuota {
				return ErrImageAsyncConflict
			}
			result = imageAsyncBillingSnapshot(task, false)
			return nil
		}
		if !imageAsyncLeaseValid(task, owner) {
			return ErrImageAsyncLeaseLost
		}
		if task.BillingState != ImageAsyncBillingReserved {
			return ErrImageAsyncBillingFinalized
		}
		user, token, err := imageAsyncBillingAccounts(tx, task, false)
		if err != nil {
			return err
		}
		delta := actualQuota - task.ReservedQuota
		if err := imageAsyncAdjustToken(tx, token, delta); err != nil {
			return err
		}
		if task.ReservedQuota == 0 && actualQuota > 0 {
			if err := chooseImageAsyncFunding(tx, task, user, actualQuota, task.BillingPreference); err != nil {
				return err
			}
		} else if err := imageAsyncAdjustFunding(tx, task, user, delta); err != nil {
			return err
		}
		task.BillingState, task.ActualQuota = ImageAsyncBillingSettled, actualQuota
		if err := saveImageAsyncBilling(tx, task); err != nil {
			return err
		}
		userID, tokenKey = task.UserID, token.Key
		result = imageAsyncBillingSnapshot(task, true)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if userID > 0 {
		invalidateImageAsyncBillingCaches(userID, tokenKey)
	}
	return result, nil
}

// refundImageAsyncQuotaTx is also used by crash recovery, in the same
// transaction that marks an expired run failed.
func refundImageAsyncQuotaTx(tx *gorm.DB, task *ImageAsyncTask) (string, error) {
	if task.BillingState == ImageAsyncBillingRefunded || task.BillingState == "" {
		return "", nil
	}
	if task.BillingState != ImageAsyncBillingReserved {
		return "", ErrImageAsyncBillingFinalized
	}
	user, token, err := imageAsyncBillingAccounts(tx, task, false)
	if err != nil {
		return "", err
	}
	if err := imageAsyncAdjustToken(tx, token, -task.ReservedQuota); err != nil {
		return "", err
	}
	if err := imageAsyncAdjustFunding(tx, task, user, -task.ReservedQuota); err != nil {
		return "", err
	}
	task.BillingState, task.ActualQuota = ImageAsyncBillingRefunded, 0
	if err := saveImageAsyncBilling(tx, task); err != nil {
		return "", err
	}
	return token.Key, nil
}

func RefundImageAsyncQuota(id, owner string) (*ImageAsyncBilling, error) {
	var result *ImageAsyncBilling
	var userID int
	var tokenKey string
	err := DB.Transaction(func(tx *gorm.DB) error {
		task, err := getImageAsyncTaskLocked(tx, id)
		if err != nil {
			return err
		}
		if task.BillingState == ImageAsyncBillingSettled {
			return ErrImageAsyncBillingFinalized
		}
		if task.BillingState == ImageAsyncBillingRefunded || task.BillingState == "" {
			result = imageAsyncBillingSnapshot(task, false)
			return nil
		}
		allowed := imageAsyncLeaseValid(task, owner)
		if owner == "" && (task.Status == ImageAsyncStatusFailed || (task.Status == ImageAsyncStatusRunning && task.LeaseUntil <= common.GetTimestamp())) {
			allowed = true
		}
		if !allowed {
			return ErrImageAsyncLeaseLost
		}
		key, err := refundImageAsyncQuotaTx(tx, task)
		if err != nil {
			return err
		}
		userID, tokenKey = task.UserID, key
		result = imageAsyncBillingSnapshot(task, true)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if userID > 0 {
		invalidateImageAsyncBillingCaches(userID, tokenKey)
	}
	return result, nil
}
