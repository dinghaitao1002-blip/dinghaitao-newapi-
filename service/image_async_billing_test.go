package service

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedAsyncImageBilling(t *testing.T) (*gin.Context, *relaycommon.RelayInfo, *model.ImageAsyncTask) {
	t.Helper()
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.ImageAsyncTask{}, &model.ImageAsyncQueueLock{}))
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("storage_node = ?", "service-test").Delete(&model.ImageAsyncTask{}).Error)
	})
	seedUser(t, 8101, 1000)
	seedToken(t, 8102, 8101, "async-service-test-key", 1000)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", 8102).Update("expired_time", -1).Error)
	seedChannel(t, 8103)
	id, err := model.GenerateImageAsyncTaskID()
	require.NoError(t, err)
	_, created, err := model.CreateImageAsyncTask(&model.ImageAsyncTask{
		ID: id, UserID: 8101, TokenID: 8102, Operation: "/v1/images/generations",
		Model: "async-service-test", Fingerprint: strings.Repeat("a", 64),
		StorageNode: "service-test", RequestID: "async-service-request", ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, 100, 10)
	require.NoError(t, err)
	require.True(t, created)
	task, err := model.ClaimImageAsyncTask("service-test", "service-owner", 3600)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, id, task.ID)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	c.Set(ImageAsyncTaskIDContextKey, task.ID)
	c.Set(ImageAsyncOwnerContextKey, "service-owner")
	c.Set("token_name", "async-test")
	c.Set(common.RequestIdKey, "async-service-request")
	info := &relaycommon.RelayInfo{
		UserId: 8101, TokenId: 8102, TokenKey: "async-service-test-key", UserQuota: common.MaxQuota,
		UserSetting: dto.UserSetting{BillingPreference: "wallet_only"}, UsingGroup: "default",
		OriginModelName: "async-service-test", StartTime: time.Now(), RelayFormat: types.RelayFormatOpenAIImage,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 8103},
		PriceData: types.PriceData{UsePrice: true, ModelPrice: 100 / common.QuotaPerUnit,
			GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}},
	}
	return c, info, task
}

func readAsyncImageBalances(t *testing.T) (model.User, model.Token) {
	t.Helper()
	var user model.User
	var token model.Token
	require.NoError(t, model.DB.First(&user, 8101).Error)
	require.NoError(t, model.DB.First(&token, 8102).Error)
	return user, token
}

func TestImageAsyncBillingSettlementAndRefundReplay(t *testing.T) {
	c, info, task := seedAsyncImageBilling(t)
	require.Nil(t, PreConsumeBilling(c, 100, info))
	assert.True(t, info.ForcePreConsume)
	require.NoError(t, info.Billing.Reserve(150))
	assert.Equal(t, 150, info.FinalPreConsumedQuota)
	require.NoError(t, info.Billing.Settle(120))
	require.NoError(t, info.Billing.Settle(120))
	info.Billing.Refund(c)
	assert.False(t, info.Billing.NeedsRefund())
	user, token := readAsyncImageBalances(t)
	assert.Equal(t, 880, user.Quota)
	assert.Equal(t, 880, token.RemainQuota)
	assert.Equal(t, 120, token.UsedQuota)
	stored, err := model.GetImageAsyncTask(task.ID, info.UserId, info.TokenId)
	require.NoError(t, err)
	assert.Equal(t, model.ImageAsyncBillingSettled, stored.BillingState)
	assert.Equal(t, 120, stored.ActualQuota)
}

func TestImageAsyncBillingRefundAndZeroReservation(t *testing.T) {
	for _, quota := range []int{0, 100} {
		t.Run(map[int]string{0: "free", 100: "paid"}[quota], func(t *testing.T) {
			c, info, task := seedAsyncImageBilling(t)
			if quota == 0 {
				info.UserSetting.BillingPreference = "subscription_only"
			}
			require.Nil(t, PreConsumeBilling(c, quota, info))
			assert.True(t, info.Billing.NeedsRefund())
			info.Billing.Refund(c)
			info.Billing.Refund(c)
			assert.False(t, info.Billing.NeedsRefund())
			user, token := readAsyncImageBalances(t)
			assert.Equal(t, 1000, user.Quota)
			assert.Equal(t, 1000, token.RemainQuota)
			assert.Zero(t, token.UsedQuota)
			stored, err := model.GetImageAsyncTask(task.ID, info.UserId, info.TokenId)
			require.NoError(t, err)
			assert.Equal(t, model.ImageAsyncBillingRefunded, stored.BillingState)
		})
	}
}

func TestImageAsyncBillingFailedSettlementDoesNotRecordSuccess(t *testing.T) {
	c, info, task := seedAsyncImageBilling(t)
	require.Nil(t, PreConsumeBilling(c, 100, info))
	require.NoError(t, model.DB.Model(&model.ImageAsyncTask{}).Where("id = ?", task.ID).Update("lease_until", time.Now().Add(-time.Minute).Unix()).Error)
	err := PostTextConsumeQuota(c, info, &dto.Usage{PromptTokens: 1, TotalTokens: 1}, nil)
	require.ErrorIs(t, err, model.ErrImageAsyncLeaseLost)
	_, exists := c.Get(ImageAsyncBillingErrorContextKey)
	assert.True(t, exists)
	user, token := readAsyncImageBalances(t)
	assert.Equal(t, 900, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	var count int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ?", info.UserId).Count(&count).Error)
	assert.Zero(t, count)
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, info.ChannelId).Error)
	assert.Zero(t, channel.UsedQuota)
}

func TestImageAsyncBillingSuccessfulReplayRecordsUsageOnce(t *testing.T) {
	c, info, _ := seedAsyncImageBilling(t)
	require.Nil(t, PreConsumeBilling(c, 150, info))
	usage := &dto.Usage{PromptTokens: 1, TotalTokens: 1}
	require.NoError(t, PostTextConsumeQuota(c, info, usage, nil))
	require.NoError(t, PostTextConsumeQuota(c, info, usage, nil))
	user, token := readAsyncImageBalances(t)
	assert.Equal(t, 900, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Equal(t, 100, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	var count int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ? AND type = ?", info.UserId, model.LogTypeConsume).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestImageAsyncResultPersistenceRequired(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.NoError(t, CommitImageAsyncResult(c))
	c.Set(ImageAsyncTaskIDContextKey, "test-task")
	require.Error(t, CommitImageAsyncResult(c))
	diskError := errors.New("disk sync failed")
	called := false
	c.Set(ImageAsyncResultCommitContextKey, func() error { called = true; return diskError })
	require.ErrorIs(t, CommitImageAsyncResult(c), diskError)
	assert.True(t, called)
}

func TestImageAsyncBillingRejectsBatchModeAndMissingReservation(t *testing.T) {
	c, info, _ := seedAsyncImageBilling(t)
	oldBatch := common.BatchUpdateEnabled
	t.Cleanup(func() { common.BatchUpdateEnabled = oldBatch })
	common.BatchUpdateEnabled = true
	apiErr := PreConsumeBilling(c, 100, info)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	require.Error(t, SettleBilling(c, info, 100))
	user, token := readAsyncImageBalances(t)
	assert.Equal(t, 1000, user.Quota)
	assert.Equal(t, 1000, token.RemainQuota)
}

func TestImageAsyncViolationFeeSettlesWithoutResultOnce(t *testing.T) {
	c, info, task := seedAsyncImageBilling(t)
	settings := model_setting.GetGrokSettings()
	oldSettings := *settings
	t.Cleanup(func() { *settings = oldSettings })
	settings.ViolationDeductionEnabled = true
	settings.ViolationDeductionAmount = 50 / common.QuotaPerUnit
	require.Nil(t, PreConsumeBilling(c, 150, info))
	c.Set(ImageAsyncResultCommitContextKey, func() error { t.Fatal("failure fee must not commit an image"); return nil })
	apiErr := types.NewErrorWithStatusCode(errors.New("test policy failure"), types.ErrorCodeViolationFeeGrokCSAM, http.StatusBadRequest)
	require.True(t, ChargeViolationFeeIfNeeded(c, info, apiErr))
	require.True(t, ChargeViolationFeeIfNeeded(c, info, apiErr))
	info.Billing.Refund(c)
	user, token := readAsyncImageBalances(t)
	assert.Equal(t, 950, user.Quota)
	assert.Equal(t, 950, token.RemainQuota)
	assert.Equal(t, 50, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	stored, err := model.GetImageAsyncTask(task.ID, info.UserId, info.TokenId)
	require.NoError(t, err)
	assert.Equal(t, model.ImageAsyncBillingSettled, stored.BillingState)
	assert.Equal(t, 50, stored.ActualQuota)
	var count int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ?", info.UserId).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestViolationFeeQuotaCannotOverflowOrPanic(t *testing.T) {
	for _, tc := range []struct {
		name          string
		amount, ratio float64
		want          int
		clamped       bool
	}{
		{"normal", 0.05, 1, common.QuotaRound(0.05 * common.QuotaPerUnit), false},
		{"negative", -1, 1, 0, false},
		{"nan", math.NaN(), 1, 0, false},
		{"infinite_ratio", 1, math.Inf(1), 0, false},
		{"huge", math.MaxFloat64, 1, common.MaxQuota, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quota, clamp := calcViolationFeeQuota(tc.amount, tc.ratio)
			assert.Equal(t, tc.want, quota)
			assert.Equal(t, tc.clamped, clamp != nil)
		})
	}
}
