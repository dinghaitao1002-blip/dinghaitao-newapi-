package service

import (
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const (
	ImageAsyncTaskIDContextKey       = "image_async_task_id"
	ImageAsyncOwnerContextKey        = "image_async_owner"
	ImageAsyncResultCommitContextKey = "image_async_result_commit"
	ImageAsyncBillingErrorContextKey = "image_async_billing_error"
)

func IsImageAsyncRequest(c *gin.Context) bool {
	return c != nil && c.GetString(ImageAsyncTaskIDContextKey) != ""
}

// CommitImageAsyncResult establishes the durable result boundary before billing.
// The worker's callback must check capture errors, sync and atomically publish
// the complete response. Ordinary synchronous requests have no result store.
func CommitImageAsyncResult(c *gin.Context) error {
	if !IsImageAsyncRequest(c) {
		return nil
	}
	value, exists := c.Get(ImageAsyncResultCommitContextKey)
	commit, ok := value.(func() error)
	if !exists || !ok || commit == nil {
		return errors.New("async image result persistence is not configured")
	}
	return commit()
}

// ImageAsyncBillingSession delegates every balance change to the task ledger.
// Its local metadata is only a projection; the database protects replay and
// lease ownership even after this object or the server process is recreated.
type ImageAsyncBillingSession struct {
	mu         sync.Mutex
	taskID     string
	owner      string
	preference string
	relayInfo  *relaycommon.RelayInfo
	metadata   model.ImageAsyncBilling
}

var _ relaycommon.BillingSettler = (*ImageAsyncBillingSession)(nil)

func newImageAsyncBillingSession(c *gin.Context, info *relaycommon.RelayInfo, quota int) (*ImageAsyncBillingSession, *types.NewAPIError) {
	if common.BatchUpdateEnabled {
		return nil, types.NewErrorWithStatusCode(errors.New("async image billing requires synchronous quota persistence"), types.ErrorCodeUpdateDataError, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
	}
	if info == nil || c.GetString(ImageAsyncOwnerContextKey) == "" {
		return nil, types.NewError(errors.New("async image billing requires a worker owner"), types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	info.ForcePreConsume = true
	session := &ImageAsyncBillingSession{
		taskID: c.GetString(ImageAsyncTaskIDContextKey), owner: c.GetString(ImageAsyncOwnerContextKey),
		preference: common.NormalizeBillingPreference(info.UserSetting.BillingPreference), relayInfo: info,
	}
	if err := session.Reserve(quota); err != nil {
		code, status := types.ErrorCodeUpdateDataError, http.StatusInternalServerError
		if errors.Is(err, model.ErrImageAsyncQuotaInsufficient) {
			code, status = types.ErrorCodeInsufficientUserQuota, http.StatusForbidden
		}
		return nil, types.NewErrorWithStatusCode(err, code, status, types.ErrOptionWithSkipRetry())
	}
	return session, nil
}

func (s *ImageAsyncBillingSession) Reserve(targetQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	metadata, err := model.ReserveImageAsyncQuota(s.taskID, s.owner, targetQuota, s.preference)
	if err != nil {
		return err
	}
	s.applyMetadata(metadata)
	return nil
}

func (s *ImageAsyncBillingSession) Settle(actualQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	metadata, err := model.SettleImageAsyncQuota(s.taskID, s.owner, actualQuota)
	if err != nil {
		return err
	}
	s.applyMetadata(metadata)
	return nil
}

// Refund intentionally completes synchronously: reporting a failed job must
// not depend on an in-memory refund goroutine surviving process shutdown.
func (s *ImageAsyncBillingSession) Refund(c *gin.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metadata.BillingState == model.ImageAsyncBillingSettled {
		return
	}
	metadata, err := model.RefundImageAsyncQuota(s.taskID, s.owner)
	if err != nil {
		if c != nil {
			c.Set(ImageAsyncBillingErrorContextKey, err)
		}
		logger.LogError(c, fmt.Sprintf("failed to refund async image task %s: %s", s.taskID, err))
		return
	}
	s.applyMetadata(metadata)
}

func (s *ImageAsyncBillingSession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metadata.BillingState == model.ImageAsyncBillingReserved
}

func (s *ImageAsyncBillingSession) GetPreConsumedQuota() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metadata.ReservedQuota
}

func (s *ImageAsyncBillingSession) settlementChanged() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metadata.BillingState == model.ImageAsyncBillingSettled && s.metadata.Changed
}

func (s *ImageAsyncBillingSession) applyMetadata(metadata *model.ImageAsyncBilling) {
	s.metadata = *metadata
	info := s.relayInfo
	info.FinalPreConsumedQuota = metadata.ReservedQuota
	info.BillingSource = metadata.FundingSource
	info.SubscriptionId = metadata.SubscriptionID
	info.SubscriptionPreConsumed = 0
	info.SubscriptionPostDelta = 0
	info.SubscriptionAmountTotal = 0
	info.SubscriptionAmountUsedAfterPreConsume = 0
	if metadata.FundingSource == BillingSourceSubscription {
		info.SubscriptionPreConsumed = int64(metadata.ReservedQuota)
		info.SubscriptionAmountTotal = metadata.SubscriptionAmountTotal
		info.SubscriptionAmountUsedAfterPreConsume = metadata.SubscriptionUsedAfter
		if metadata.BillingState == model.ImageAsyncBillingSettled {
			info.SubscriptionPostDelta = int64(metadata.ActualQuota - metadata.ReservedQuota)
			// Existing log/notification helpers add PostDelta to this baseline.
			// Anchor it to the ledger snapshot, including across period resets.
			info.SubscriptionAmountUsedAfterPreConsume -= info.SubscriptionPostDelta
		}
	}
}

// imageAsyncSettlementReplayed suppresses non-idempotent usage/log side effects
// when the durable ledger reports an already committed settlement.
func imageAsyncSettlementReplayed(info *relaycommon.RelayInfo) bool {
	session, ok := info.Billing.(*ImageAsyncBillingSession)
	return ok && !session.settlementChanged()
}
