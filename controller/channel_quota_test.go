package controller

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestQuotaFailoverRespectsRequestSafety(t *testing.T) {
	err := types.NewOpenAIError(errors.New("user quota is not enough"), types.ErrorCodeBadResponseStatusCode, 402)
	for _, scenario := range []string{"replayable", "written", "pinned", "cancelled", "skip-retry"} {
		t.Run(scenario, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
			requestErr := err
			switch scenario {
			case "written":
				c.Writer.WriteHeaderNow()
			case "pinned":
				c.Set("specific_channel_id", 92)
			case "cancelled":
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			case "skip-retry":
				requestErr = types.NewOpenAIError(errors.New("user quota is not enough"), types.ErrorCodeBadResponseStatusCode, 402, types.ErrOptionWithSkipRetry())
			}
			assert.Equal(t, scenario == "replayable", shouldRetryQuotaExhaustion(c, requestErr))
		})
	}
}

func TestQuotaFailoverCanUseNextAttemptAfterOrdinaryRetriesExhausted(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
	err := types.NewOpenAIError(errors.New("user quota is not enough"), types.ErrorCodeBadResponseStatusCode, 402)
	assert.False(t, shouldRetry(c, err, 0))
	assert.True(t, shouldRetryQuotaExhaustion(c, err))
	retry := &service.RetryParam{Retry: common.GetPointer(2)}
	retry.ResetRetryNextTry()
	retry.IncreaseRetry()
	assert.Equal(t, 2, retry.GetRetry())
	retry.IncreaseRetry()
	assert.Equal(t, 3, retry.GetRetry())
}
