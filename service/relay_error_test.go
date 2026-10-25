package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldRetryRelayErrorHonorsChannelPinOnChannelError(t *testing.T) {
	err := types.NewError(errors.New("channel failed"), types.ErrorCodeChannelNoAvailableKey)
	for _, test := range []struct {
		name      string
		pin       *dto.ChannelPin
		wantRetry bool
	}{
		{name: "unrestricted channel error", wantRetry: true},
		{
			name: "single attempt pin suppresses channel error retry",
			pin: &dto.ChannelPin{
				ChannelId: 1, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt,
			},
			wantRetry: false,
		},
		{
			name: "origin task pin permits retry on the same channel",
			pin: &dto.ChannelPin{
				ChannelId: 1, Source: dto.PinSourceOriginTask, Rank: dto.PinRankOriginTask, RetryMode: dto.PinRetrySameChannel,
			},
			wantRetry: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if test.pin != nil {
				GetChannelConstraints(c).AddPin(*test.pin)
			}
			assert.Equal(t, test.wantRetry, ShouldRetryRelayError(c, err, 1))
		})
	}
}

func TestProcessChannelErrorMasksLogAndPreservesManualChannelStatus(t *testing.T) {
	truncate(t)
	previousErrorLog := constant.ErrorLogEnabled
	t.Cleanup(func() { constant.ErrorLogEnabled = previousErrorLog })
	constant.ErrorLogEnabled = true
	seedUser(t, 8901, 10000)
	seedChannel(t, 8902)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 8902).Update("status", common.ChannelStatusEnabled).Error)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("id", 8901)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://private.example.com")
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream https://private.example.com/path?token=review-token api_key:review-secret"), types.ErrorCodeChannelNoAvailableKey, http.StatusUnauthorized)
	ProcessChannelError(c, types.ChannelError{ChannelId: 8902, ChannelName: "relay-review", AutoBan: true}, apiErr, nil)
	loaded, err := model.GetChannelById(8902, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, loaded.Status)
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Contains(t, log.Content, "status_code=401, upstream https://***.com/***?token=*** api_key:***")
	assert.NotContains(t, log.Content, "review-token")
	assert.NotContains(t, log.Content, "review-secret")
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
}

// Only addresses the admin configured are masked: a channel on the base URL
// new-api presets reaches a public official endpoint, and so do adaptors that
// build official endpoints themselves.
func TestUpstreamAddressesMaskOnlyConfiguredAddresses(t *testing.T) {
	for _, tc := range []struct {
		name        string
		channelType int
		baseURL     string
		origin      string
		proxyEnv    string
		message     string
		want        string
	}{
		{
			name:        "preset base URL stays readable",
			channelType: constant.ChannelTypeAnthropic,
			baseURL:     "https://api.anthropic.com",
			origin:      "https://api.anthropic.com",
			message:     "request to https://api.anthropic.com/v1/messages failed, see https://platform.claude.com/docs/en/build-with-claude/task-budgets",
			want:        "request to https://api.anthropic.com/v1/messages failed, see https://platform.claude.com/docs/en/build-with-claude/task-budgets",
		},
		{
			name:        "official endpoint built by the adaptor stays readable",
			channelType: constant.ChannelTypeVertexAi,
			origin:      "https://us-central1-aiplatform.googleapis.com",
			message:     "request to https://us-central1-aiplatform.googleapis.com/v1/projects failed",
			want:        "request to https://us-central1-aiplatform.googleapis.com/v1/projects failed",
		},
		{
			name:        "configured base URL is masked",
			channelType: constant.ChannelTypeOpenAI,
			baseURL:     "https://relay.private.example",
			origin:      "https://relay.private.example",
			message:     "request to https://relay.private.example/v1/chat/completions failed",
			want:        "request to https://***.example/***/***/*** failed",
		},
		{
			name:        "absolute Advanced Custom route is masked",
			channelType: constant.ChannelTypeAdvancedCustom,
			origin:      "https://route.private.example",
			message:     "dial tcp: lookup route.private.example: no such host",
			want:        "dial tcp: lookup ***.***.example: no such host",
		},
		{
			name:        "environment proxy is masked on a preset channel",
			channelType: constant.ChannelTypeAnthropic,
			baseURL:     "https://api.anthropic.com",
			proxyEnv:    "http://egress.private.invalid:8080",
			message:     "proxyconnect tcp: dial tcp: lookup egress.private.invalid: no such host",
			want:        "proxyconnect tcp: dial tcp: lookup ***.***.invalid: no such host",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HTTPS_PROXY", tc.proxyEnv)
			t.Setenv("HTTP_PROXY", "")
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			common.SetContextKey(c, constant.ContextKeyChannelType, tc.channelType)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, tc.baseURL)
			common.SetContextKey(c, constant.ContextKeyUpstreamRequestOrigin, tc.origin)
			apiErr := types.NewErrorWithStatusCode(errors.New(tc.message), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

			assert.Equal(t, tc.want, apiErr.ToOpenAIError(common.UpstreamAddresses(c)...).Message)
		})
	}
}

func TestDecideRelayRetryReasons(t *testing.T) {
	upstream := func(status int) *types.NewAPIError {
		return types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, status)
	}
	for _, tc := range []struct {
		name    string
		err     *types.NewAPIError
		retries int
		setup   func(*gin.Context)
		want    PolicyDecision
	}{
		{name: "retry status matched", err: upstream(http.StatusTooManyRequests), retries: 1, want: PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}},
		{name: "status outside retry rules", err: upstream(http.StatusBadRequest), retries: 1, want: PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}},
		{name: "attempt budget exhausted", err: upstream(http.StatusTooManyRequests), retries: 0, want: PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}},
		{name: "always skipped status", err: upstream(http.StatusGatewayTimeout), retries: 1, want: PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}},
		{name: "success status never retries", err: upstream(http.StatusOK), retries: 1, want: PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}},
		{name: "skip retry error", err: types.NewErrorWithStatusCode(errors.New("local"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry()), retries: 1, want: PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}},
		{name: "channel error retries without budget", err: types.NewError(errors.New("no key"), types.ErrorCodeChannelNoAvailableKey), retries: 0, want: PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}},
		{name: "single attempt pin", err: upstream(http.StatusTooManyRequests), retries: 1, setup: func(c *gin.Context) {
			GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: 1, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
		}, want: PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}},
		{name: "strict session", err: upstream(http.StatusTooManyRequests), retries: 1, setup: func(c *gin.Context) {
			c.Set(ginKeyChannelAffinitySkipRetry, true)
			RequestPolicy(c).SessionModeSource = "global"
		}, want: PolicyDecision{Action: "stop", Reason: "strict_session", Source: "global"}},
		{name: "nil error", retries: 1, want: PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if tc.setup != nil {
				tc.setup(c)
			}
			decision := DecideRelayRetry(c, tc.err, tc.retries)
			assert.Equal(t, tc.want, decision)
			assert.Equal(t, tc.want.Action == "retry", ShouldRetryRelayError(c, tc.err, tc.retries))
		})
	}
}

func TestRequestPolicyEventsReachLogAdminInfo(t *testing.T) {
	previousAutoDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previousAutoDisable })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("auto_ban", true)
	c.Set("channel_id", 7)
	state := RequestPolicy(c)
	state.BeginAttempt(&model.Channel{Id: 7}, "default")
	apiErr := types.NewOpenAIError(errors.New("invalid credential"), types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized)
	RecordPolicyFailure(c, 7, apiErr, DecideRelayRetry(c, apiErr, 0))

	failed := model.NewLogOther()
	AppendRelayLogAdminInfo(c, nil, failed)
	events, ok := failed.Snapshot()["admin_info"].(map[string]any)["request_policy"].([]PolicyEvent)
	require.True(t, ok, "a failed relay exposes its decision events to administrators")
	require.Len(t, events, 3)
	assert.Equal(t, PolicyDecision{Action: "attempt", Reason: "channel_selected", Source: "routing"}, events[0].Decision)
	assert.Equal(t, "default", events[0].Group)
	assert.Equal(t, PolicyDecision{Action: "failure", Reason: "upstream_failure", Source: "upstream"}, events[1].Decision)
	assert.Equal(t, http.StatusUnauthorized, events[1].Status)
	assert.Equal(t, PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}, events[2].Decision)
	assert.Equal(t, "unchanged", events[2].Health, "automatic disabling is disabled by the production policy")
	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, true)
	RecordPolicyFailure(c, 7, apiErr, DecideRelayRetry(c, apiErr, 0))
	assert.Equal(t, "unchanged", state.Events()[4].Health)

	state.BeginAttempt(&model.Channel{Id: 8}, "default")
	c.Set("channel_id", 8)
	MarkRequestPolicySuccess(c, nil)
	MarkRequestPolicySuccess(c, nil)
	succeeded := model.NewLogOther()
	AppendRelayLogAdminInfo(c, nil, succeeded)
	events, ok = succeeded.Snapshot()["admin_info"].(map[string]any)["request_policy"].([]PolicyEvent)
	require.True(t, ok, "a successful relay exposes its decision events to administrators")
	require.Len(t, events, 7, "the outcome is recorded once")
	assert.Equal(t, PolicyDecision{Action: "success", Reason: "request_completed", Source: "upstream"}, events[6].Decision)
	assert.Equal(t, 8, events[6].ChannelID)
	assert.Equal(t, 2, events[6].Attempt)
	assert.True(t, state.Successful)

	untouched, _ := gin.CreateTestContext(httptest.NewRecorder())
	other := model.NewLogOther()
	AppendRelayLogAdminInfo(untouched, nil, other)
	assert.NotContains(t, other.Snapshot()["admin_info"], "request_policy", "requests without decisions do not carry an empty record")
}

func TestRelayTimeoutFailoverPreservesAsyncAndOutputBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		async, written, cancelled, wantRetry bool
	}{
		{name: "synchronous timeout may fail over", wantRetry: true},
		{name: "async timeout is ambiguous", async: true},
		{name: "response already started", written: true},
		{name: "client cancelled", cancelled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			if tc.async {
				c.Set(ImageAsyncTaskIDContextKey, "imgtask_test")
			}
			if tc.written {
				c.Writer.WriteHeaderNow()
			}
			if tc.cancelled {
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			}
			err := types.NewErrorWithStatusCode(context.DeadlineExceeded, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
			assert.Equal(t, tc.wantRetry, ShouldRetryRelayError(c, err, 1))
		})
	}
}
