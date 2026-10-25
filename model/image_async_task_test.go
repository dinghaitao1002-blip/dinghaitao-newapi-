package model

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

var imageAsyncTestSequence atomic.Int64

func imageAsyncTestAccounts(t *testing.T, quota, tokenQuota int, unlimited bool) (*User, *Token) {
	t.Helper()
	n := imageAsyncTestSequence.Add(1)
	user := &User{Username: fmt.Sprintf("async-%d", n), AffCode: fmt.Sprintf("ia%d", n), Status: common.UserStatusEnabled, Quota: quota}
	require.NoError(t, DB.Create(user).Error)
	token := &Token{UserId: user.Id, Key: fmt.Sprintf("async-test-token-%d", n), Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: tokenQuota, UnlimitedQuota: unlimited}
	require.NoError(t, DB.Create(token).Error)
	return user, token
}

func imageAsyncTestTask(t *testing.T, user *User, token *Token, node string, key *string) *ImageAsyncTask {
	t.Helper()
	id, err := GenerateImageAsyncTaskID()
	require.NoError(t, err)
	return &ImageAsyncTask{ID: id, UserID: user.Id, TokenID: token.Id, StorageNode: node, Operation: "/v1/images/generations", Model: "test-image", ContentType: "application/json", Fingerprint: strings.Repeat("a", 64), IdempotencyHash: key, ExpiresAt: common.GetTimestamp() + 3600}
}

func imageAsyncTestRunning(t *testing.T, user *User, token *Token) *ImageAsyncTask {
	t.Helper()
	task := imageAsyncTestTask(t, user, token, fmt.Sprintf("node-%d", imageAsyncTestSequence.Add(1)), nil)
	_, created, err := CreateImageAsyncTask(task, 100, 100)
	require.NoError(t, err)
	require.True(t, created)
	claimed, err := ClaimImageAsyncTask(task.StorageNode, "test-worker", 600)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, task.ID, claimed.ID)
	return claimed
}

func imageAsyncTestBalances(t *testing.T, user *User, token *Token, wallet, remaining, used int) {
	t.Helper()
	var gotUser User
	var gotToken Token
	require.NoError(t, DB.Unscoped().First(&gotUser, user.Id).Error)
	require.NoError(t, DB.Unscoped().First(&gotToken, token.Id).Error)
	require.Equal(t, wallet, gotUser.Quota, "wallet")
	require.Equal(t, remaining, gotToken.RemainQuota, "token remaining")
	require.Equal(t, used, gotToken.UsedQuota, "token used")
}

func imageAsyncTestSubscription(t *testing.T, user *User, total, used int64, overflow bool) *UserSubscription {
	t.Helper()
	plan := &SubscriptionPlan{Title: "Async test", QuotaResetPeriod: "never", TotalAmount: total}
	require.NoError(t, DB.Create(plan).Error)
	sub := &UserSubscription{UserId: user.Id, PlanId: plan.Id, Status: "active", StartTime: common.GetTimestamp() - 100, EndTime: common.GetTimestamp() + 3600, AmountTotal: total, AmountUsed: used, AllowWalletOverflow: overflow, LastResetTime: common.GetTimestamp() - 100}
	require.NoError(t, DB.Create(sub).Error)
	return sub
}

func TestImageAsyncAdmissionIdempotencyAndOwnership(t *testing.T) {
	truncateTables(t)
	u, tok := imageAsyncTestAccounts(t, 1000, 1000, false)
	key := strings.Repeat("b", 64)
	input := imageAsyncTestTask(t, u, tok, "node-a", &key)
	input.BillingState, input.ReservedQuota, input.Owner = ImageAsyncBillingSettled, 999, "untrusted"
	first, created, err := CreateImageAsyncTask(input, 1, 1)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "", first.BillingState)
	require.Zero(t, first.ReservedQuota)
	require.Empty(t, first.Owner)
	replay := imageAsyncTestTask(t, u, tok, "node-a", &key)
	again, created, err := CreateImageAsyncTask(replay, 1, 1)
	require.NoError(t, err, "idempotent retries bypass queue caps")
	require.False(t, created)
	require.Equal(t, first.ID, again.ID)
	replay.Fingerprint = strings.Repeat("c", 64)
	_, _, err = CreateImageAsyncTask(replay, 1, 1)
	require.ErrorIs(t, err, ErrImageAsyncConflict)
	_, _, err = CreateImageAsyncTask(imageAsyncTestTask(t, u, tok, "node-a", nil), 10, 1)
	require.ErrorIs(t, err, ErrImageAsyncCapacity)
	u2, tok2 := imageAsyncTestAccounts(t, 1000, 1000, false)
	_, _, err = CreateImageAsyncTask(imageAsyncTestTask(t, u2, tok2, "node-b", nil), 1, 5)
	require.ErrorIs(t, err, ErrImageAsyncCapacity, "global cap spans nodes/users")
	for _, ids := range [][2]int{{u2.Id, tok.Id}, {u.Id, tok2.Id}, {0, tok.Id}} {
		got, err := GetImageAsyncTask(first.ID, ids[0], ids[1])
		require.NoError(t, err)
		require.Nil(t, got)
	}
	got, err := GetImageAsyncTask(first.ID, u.Id, tok.Id)
	require.NoError(t, err)
	require.NotNil(t, got)
}

func TestImageAsyncConcurrentAdmissionAndClaims(t *testing.T) {
	truncateTables(t)
	u, tok := imageAsyncTestAccounts(t, 1000, 1000, false)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		task := imageAsyncTestTask(t, u, tok, "shared-node", nil)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, err := CreateImageAsyncTask(task, 4, 4)
			if err == nil && created {
				admitted.Add(1)
			} else if err != ErrImageAsyncCapacity {
				t.Errorf("unexpected enqueue error: %v", err)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 4, admitted.Load())
	got, err := ClaimImageAsyncTask("other-node", "other-worker", 600)
	require.NoError(t, err)
	require.Nil(t, got)
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		owner := fmt.Sprintf("worker-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, err := ClaimImageAsyncTask("shared-node", owner, 600)
			if err != nil {
				t.Errorf("claim error: %v", err)
				return
			}
			if task != nil {
				ids <- task.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		require.False(t, seen[id], "same job claimed twice")
		seen[id] = true
	}
	require.Len(t, seen, 4)
}

func TestImageAsyncWalletReserveSettleIdempotent(t *testing.T) {
	truncateTables(t)
	u, tok := imageAsyncTestAccounts(t, 1000, 1000, false)
	task := imageAsyncTestRunning(t, u, tok)
	zero, err := ReserveImageAsyncQuota(task.ID, task.Owner, 0, "wallet_only")
	require.NoError(t, err)
	require.True(t, zero.Changed)
	require.Equal(t, ImageAsyncBillingReserved, zero.BillingState)
	imageAsyncTestBalances(t, u, tok, 1000, 1000, 0)
	meta, err := ReserveImageAsyncQuota(task.ID, task.Owner, 100, "wallet_only")
	require.NoError(t, err)
	require.True(t, meta.Changed)
	meta, err = ReserveImageAsyncQuota(task.ID, task.Owner, 100, "wallet_only")
	require.NoError(t, err)
	require.False(t, meta.Changed)
	_, err = ReserveImageAsyncQuota(task.ID, task.Owner, 150, "wallet_only")
	require.NoError(t, err)
	imageAsyncTestBalances(t, u, tok, 850, 850, 150)
	meta, err = SettleImageAsyncQuota(task.ID, task.Owner, 120)
	require.NoError(t, err)
	require.True(t, meta.Changed)
	require.Equal(t, 150, meta.ReservedQuota)
	require.Equal(t, 120, meta.ActualQuota)
	imageAsyncTestBalances(t, u, tok, 880, 880, 120)
	meta, err = SettleImageAsyncQuota(task.ID, task.Owner, 120)
	require.NoError(t, err)
	require.False(t, meta.Changed)
	_, err = SettleImageAsyncQuota(task.ID, task.Owner, 121)
	require.ErrorIs(t, err, ErrImageAsyncConflict)
	_, err = RefundImageAsyncQuota(task.ID, task.Owner)
	require.ErrorIs(t, err, ErrImageAsyncBillingFinalized)
	require.NoError(t, CompleteImageAsyncTask(task.ID, task.Owner, 200, "", ""))
	require.NoError(t, CompleteImageAsyncTask(task.ID, task.Owner, 200, "", ""))
	meta, err = SettleImageAsyncQuota(task.ID, task.Owner, 120)
	require.NoError(t, err)
	require.False(t, meta.Changed)
	imageAsyncTestBalances(t, u, tok, 880, 880, 120)
	var user User
	require.NoError(t, DB.First(&user, u.Id).Error)
	require.Zero(t, user.UsedQuota, "relay owns aggregate usage stats, not durable quota accounting")
}

func TestImageAsyncQuotaFailuresRollback(t *testing.T) {
	for _, tc := range []struct {
		name          string
		wallet, token int
	}{{"token", 1000, 10}, {"wallet", 10, 1000}} {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			u, tok := imageAsyncTestAccounts(t, tc.wallet, tc.token, false)
			task := imageAsyncTestRunning(t, u, tok)
			_, err := ReserveImageAsyncQuota(task.ID, task.Owner, 100, "wallet_only")
			require.ErrorIs(t, err, ErrImageAsyncQuotaInsufficient)
			imageAsyncTestBalances(t, u, tok, tc.wallet, tc.token, 0)
			got, err := GetImageAsyncTask(task.ID, u.Id, tok.Id)
			require.NoError(t, err)
			require.Empty(t, got.BillingState)
		})
	}
}

func TestImageAsyncRejectsOutOfRangeQuota(t *testing.T) {
	truncateTables(t)
	u, tok := imageAsyncTestAccounts(t, 1000, 1000, false)
	task := imageAsyncTestRunning(t, u, tok)
	for _, amount := range []int{-1, common.MaxQuota + 1} {
		_, err := ReserveImageAsyncQuota(task.ID, task.Owner, amount, "wallet_only")
		require.Error(t, err)
		_, err = SettleImageAsyncQuota(task.ID, task.Owner, amount)
		require.Error(t, err)
	}
	imageAsyncTestBalances(t, u, tok, 1000, 1000, 0)
}

func TestImageAsyncLeaseFencingAndAtomicRecoveryRefund(t *testing.T) {
	truncateTables(t)
	u, tok := imageAsyncTestAccounts(t, 1000, 1000, false)
	task := imageAsyncTestRunning(t, u, tok)
	_, err := ReserveImageAsyncQuota(task.ID, task.Owner, 100, "wallet_only")
	require.NoError(t, err)
	require.NoError(t, RenewImageAsyncTaskLease(task.ID, task.Owner, 600))
	require.NoError(t, RenewImageAsyncTaskLease(task.ID, task.Owner, 600))
	_, err = SettleImageAsyncQuota(task.ID, "wrong-owner", 100)
	require.ErrorIs(t, err, ErrImageAsyncLeaseLost)
	require.NoError(t, DB.Model(task).UpdateColumn("lease_until", common.GetTimestamp()-1).Error)
	_, err = SettleImageAsyncQuota(task.ID, task.Owner, 100)
	require.ErrorIs(t, err, ErrImageAsyncLeaseLost)
	require.ErrorIs(t, RenewImageAsyncTaskLease(task.ID, task.Owner, 600), ErrImageAsyncLeaseLost)
	require.ErrorIs(t, CompleteImageAsyncTask(task.ID, task.Owner, 200, "", ""), ErrImageAsyncLeaseLost)
	expired, err := ListExpiredRunningImageTasks(task.StorageNode)
	require.NoError(t, err)
	require.Len(t, expired, 1)
	changed, err := RecoverExpiredImageAsyncTask(task.ID, "wrong-node", false, "interrupted", "")
	require.NoError(t, err)
	require.False(t, changed)
	changed, err = RecoverExpiredImageAsyncTask(task.ID, task.StorageNode, false, "interrupted", "Worker stopped")
	require.NoError(t, err)
	require.True(t, changed)
	got, err := GetImageAsyncTask(task.ID, u.Id, tok.Id)
	require.NoError(t, err)
	require.Equal(t, ImageAsyncStatusFailed, got.Status)
	require.Equal(t, ImageAsyncBillingRefunded, got.BillingState)
	changed, err = RecoverExpiredImageAsyncTask(task.ID, task.StorageNode, false, "interrupted", "")
	require.NoError(t, err)
	require.False(t, changed)
	meta, err := RefundImageAsyncQuota(task.ID, "")
	require.NoError(t, err)
	require.False(t, meta.Changed)
	imageAsyncTestBalances(t, u, tok, 1000, 1000, 0)
}

func TestImageAsyncSettledRecoveryDoesNotRefund(t *testing.T) {
	truncateTables(t)
	u, tok := imageAsyncTestAccounts(t, 1000, 1000, false)
	task := imageAsyncTestRunning(t, u, tok)
	_, err := ReserveImageAsyncQuota(task.ID, task.Owner, 100, "wallet_only")
	require.NoError(t, err)
	_, err = SettleImageAsyncQuota(task.ID, task.Owner, 100)
	require.NoError(t, err)
	require.ErrorIs(t, CompleteImageAsyncTask(task.ID, task.Owner, 500, "commit_unknown", ""), ErrImageAsyncBillingFinalized,
		"ambiguous settlement must remain recoverable, not fail a paid task")
	require.NoError(t, DB.Model(task).UpdateColumn("lease_until", common.GetTimestamp()-1).Error)
	changed, err := RecoverExpiredImageAsyncTask(task.ID, task.StorageNode, true, "", "")
	require.NoError(t, err)
	require.True(t, changed)
	got, err := GetImageAsyncTask(task.ID, u.Id, tok.Id)
	require.NoError(t, err)
	require.Equal(t, ImageAsyncStatusSucceeded, got.Status)
	require.Equal(t, ImageAsyncBillingSettled, got.BillingState)
	imageAsyncTestBalances(t, u, tok, 900, 900, 100)
}

func TestImageAsyncFailedRefundScanAndTTL(t *testing.T) {
	truncateTables(t)
	u, tok := imageAsyncTestAccounts(t, 1000, 1000, false)
	task := imageAsyncTestRunning(t, u, tok)
	_, err := ReserveImageAsyncQuota(task.ID, task.Owner, 100, "wallet_only")
	require.NoError(t, err)
	require.NoError(t, CompleteImageAsyncTask(task.ID, task.Owner, 502, "upstream", "failed"))
	require.NoError(t, DB.Model(task).UpdateColumn("expires_at", common.GetTimestamp()-1).Error)
	pending, err := ListImageAsyncRefundPending(task.StorageNode, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	cleanup, err := ListExpiredImageAsyncTasks(task.StorageNode, 10)
	require.NoError(t, err)
	require.Empty(t, cleanup, "reserved funds must never lose their durable ledger")
	require.Error(t, DeleteExpiredImageAsyncTask(task.ID, task.StorageNode))
	_, err = RefundImageAsyncQuota(task.ID, "")
	require.NoError(t, err)
	cleanup, err = ListExpiredImageAsyncTasks(task.StorageNode, 10)
	require.NoError(t, err)
	require.Len(t, cleanup, 1)
	require.Error(t, DeleteExpiredImageAsyncTask(task.ID, "wrong-node"))
	require.NoError(t, DeleteExpiredImageAsyncTask(task.ID, task.StorageNode))
	imageAsyncTestBalances(t, u, tok, 1000, 1000, 0)
}

func TestImageAsyncExplicitViolationFeeCanFinishFailed(t *testing.T) {
	truncateTables(t)
	u, tok := imageAsyncTestAccounts(t, 1000, 1000, false)
	task := imageAsyncTestRunning(t, u, tok)
	_, err := ReserveImageAsyncQuota(task.ID, task.Owner, 100, "wallet_only")
	require.NoError(t, err)
	_, err = SettleImageAsyncQuota(task.ID, task.Owner, 10)
	require.NoError(t, err)
	require.NoError(t, CompleteImageAsyncTask(task.ID, task.Owner, 400, "violation_fee.grok_csam", "Request rejected"))
	got, err := GetImageAsyncTask(task.ID, u.Id, tok.Id)
	require.NoError(t, err)
	require.Equal(t, ImageAsyncStatusFailed, got.Status)
	require.Equal(t, ImageAsyncBillingSettled, got.BillingState)
	_, err = RefundImageAsyncQuota(task.ID, "")
	require.ErrorIs(t, err, ErrImageAsyncBillingFinalized)
	imageAsyncTestBalances(t, u, tok, 990, 990, 10)
}

func TestImageAsyncUnlimitedTokenAndDeletedAccountRefund(t *testing.T) {
	truncateTables(t)
	u, tok := imageAsyncTestAccounts(t, 1000, 0, true)
	task := imageAsyncTestRunning(t, u, tok)
	_, err := ReserveImageAsyncQuota(task.ID, task.Owner, 100, "wallet_only")
	require.NoError(t, err)
	imageAsyncTestBalances(t, u, tok, 900, -100, 100)
	require.NoError(t, DB.Delete(tok).Error)
	require.NoError(t, DB.Delete(u).Error)
	_, err = RefundImageAsyncQuota(task.ID, task.Owner)
	require.NoError(t, err, "soft deletion cannot strand reserved money")
	imageAsyncTestBalances(t, u, tok, 1000, 0, 0)
}

func TestImageAsyncSubscriptionPreferencesAndOverflow(t *testing.T) {
	cases := []struct {
		name, preference, source string
		wallet                   int
		total, used              int64
		overflow, wantErr        bool
	}{
		{"subscription-first", "subscription_first", "subscription", 1000, 500, 0, false, false},
		{"wallet-first", "wallet_first", "wallet", 1000, 500, 0, false, false},
		{"wallet-fallback-subscription", "wallet_first", "subscription", 0, 500, 0, false, false},
		{"subscription-only", "subscription_only", "subscription", 1000, 500, 0, false, false},
		{"subscription-exhausted-block", "subscription_first", "", 1000, 500, 500, false, true},
		{"subscription-overflow-wallet", "subscription_first", "wallet", 1000, 500, 500, true, false},
		{"subscription-only-no-overflow", "subscription_only", "", 1000, 500, 500, true, true},
		{"wallet-only-no-fallback", "wallet_only", "", 0, 500, 0, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			u, tok := imageAsyncTestAccounts(t, tc.wallet, 1000, false)
			sub := imageAsyncTestSubscription(t, u, tc.total, tc.used, tc.overflow)
			task := imageAsyncTestRunning(t, u, tok)
			meta, err := ReserveImageAsyncQuota(task.ID, task.Owner, 100, tc.preference)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrImageAsyncQuotaInsufficient)
				imageAsyncTestBalances(t, u, tok, tc.wallet, 1000, 0)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.source, meta.FundingSource)
			wallet, used := tc.wallet, tc.used
			if tc.source == "wallet" {
				wallet -= 100
			} else {
				used += 100
				require.Equal(t, sub.Id, meta.SubscriptionID)
			}
			imageAsyncTestBalances(t, u, tok, wallet, 900, 100)
			var saved UserSubscription
			require.NoError(t, DB.First(&saved, sub.Id).Error)
			require.Equal(t, used, saved.AmountUsed)
			_, err = SettleImageAsyncQuota(task.ID, task.Owner, 80)
			require.NoError(t, err)
			if tc.source == "wallet" {
				wallet += 20
			} else {
				used -= 20
			}
			imageAsyncTestBalances(t, u, tok, wallet, 920, 80)
			require.NoError(t, DB.First(&saved, sub.Id).Error)
			require.Equal(t, used, saved.AmountUsed)
		})
	}
}

func TestImageAsyncSubscriptionResetDoesNotRefundNewPeriod(t *testing.T) {
	truncateTables(t)
	u, tok := imageAsyncTestAccounts(t, 1000, 1000, false)
	sub := imageAsyncTestSubscription(t, u, 500, 0, false)
	task := imageAsyncTestRunning(t, u, tok)
	_, err := ReserveImageAsyncQuota(task.ID, task.Owner, 100, "subscription_only")
	require.NoError(t, err)
	// Simulate the normal reset scheduler and a later request in the new period.
	require.NoError(t, DB.Model(sub).Updates(map[string]any{"last_reset_time": sub.LastResetTime + 1, "amount_used": 30}).Error)
	_, err = RefundImageAsyncQuota(task.ID, task.Owner)
	require.NoError(t, err)
	var saved UserSubscription
	require.NoError(t, DB.First(&saved, sub.Id).Error)
	require.EqualValues(t, 30, saved.AmountUsed)
	imageAsyncTestBalances(t, u, tok, 1000, 1000, 0)
}

func TestImageAsyncSubscriptionSupplementAcrossResetRefundsOnlyCurrentPart(t *testing.T) {
	for _, settle := range []bool{false, true} {
		t.Run(fmt.Sprintf("settle-%v", settle), func(t *testing.T) {
			truncateTables(t)
			u, tok := imageAsyncTestAccounts(t, 1000, 1000, false)
			sub := imageAsyncTestSubscription(t, u, 500, 0, false)
			task := imageAsyncTestRunning(t, u, tok)
			_, err := ReserveImageAsyncQuota(task.ID, task.Owner, 100, "subscription_only")
			require.NoError(t, err)
			require.NoError(t, DB.Model(sub).Updates(map[string]any{"last_reset_time": sub.LastResetTime + 1, "amount_used": 30}).Error)
			_, err = ReserveImageAsyncQuota(task.ID, task.Owner, 200, "subscription_only")
			require.NoError(t, err)
			if settle {
				_, err = SettleImageAsyncQuota(task.ID, task.Owner, 50)
				imageAsyncTestBalances(t, u, tok, 1000, 950, 50)
			} else {
				_, err = RefundImageAsyncQuota(task.ID, task.Owner)
				imageAsyncTestBalances(t, u, tok, 1000, 1000, 0)
			}
			require.NoError(t, err)
			var saved UserSubscription
			require.NoError(t, DB.First(&saved, sub.Id).Error)
			require.EqualValues(t, 30, saved.AmountUsed, "new period retains other requests, but refunds this task's supplement")
		})
	}
}

func TestImageAsyncZeroReservationRetainsBillingPreference(t *testing.T) {
	t.Run("free-no-subscription-required", func(t *testing.T) {
		truncateTables(t)
		u, tok := imageAsyncTestAccounts(t, 0, 0, false)
		task := imageAsyncTestRunning(t, u, tok)
		_, err := ReserveImageAsyncQuota(task.ID, task.Owner, 0, "subscription_only")
		require.NoError(t, err)
		meta, err := SettleImageAsyncQuota(task.ID, task.Owner, 0)
		require.NoError(t, err)
		require.Equal(t, ImageAsyncBillingSettled, meta.BillingState)
		imageAsyncTestBalances(t, u, tok, 0, 0, 0)
	})
	t.Run("positive-settlement-uses-subscription", func(t *testing.T) {
		truncateTables(t)
		u, tok := imageAsyncTestAccounts(t, 1000, 1000, false)
		sub := imageAsyncTestSubscription(t, u, 500, 0, false)
		task := imageAsyncTestRunning(t, u, tok)
		_, err := ReserveImageAsyncQuota(task.ID, task.Owner, 0, "subscription_only")
		require.NoError(t, err)
		meta, err := SettleImageAsyncQuota(task.ID, task.Owner, 100)
		require.NoError(t, err)
		require.Equal(t, "subscription", meta.FundingSource)
		require.Equal(t, sub.Id, meta.SubscriptionID)
		imageAsyncTestBalances(t, u, tok, 1000, 900, 100)
	})
	t.Run("positive-reservation-does-not-change-preference", func(t *testing.T) {
		truncateTables(t)
		u, tok := imageAsyncTestAccounts(t, 1000, 1000, false)
		task := imageAsyncTestRunning(t, u, tok)
		_, err := ReserveImageAsyncQuota(task.ID, task.Owner, 0, "subscription_only")
		require.NoError(t, err)
		_, err = ReserveImageAsyncQuota(task.ID, task.Owner, 100, "wallet_only")
		require.ErrorIs(t, err, ErrImageAsyncQuotaInsufficient)
		imageAsyncTestBalances(t, u, tok, 1000, 1000, 0)
	})
}

func TestImageAsyncExpiredQueueIsNotClaimed(t *testing.T) {
	truncateTables(t)
	u, tok := imageAsyncTestAccounts(t, 1000, 1000, false)
	task := imageAsyncTestTask(t, u, tok, "expired-node", nil)
	_, _, err := CreateImageAsyncTask(task, 5, 5)
	require.NoError(t, err)
	require.NoError(t, DB.Model(task).UpdateColumn("expires_at", common.GetTimestamp()-1).Error)
	claimed, err := ClaimImageAsyncTask(task.StorageNode, "worker", 600)
	require.NoError(t, err)
	require.Nil(t, claimed)
	n, err := ExpireQueuedImageAsyncTasks(task.StorageNode)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	got, err := GetImageAsyncTask(task.ID, u.Id, tok.Id)
	require.NoError(t, err)
	require.Equal(t, ImageAsyncStatusFailed, got.Status)
	require.Equal(t, 410, got.HTTPStatus)
	require.Empty(t, got.BillingState)
}
