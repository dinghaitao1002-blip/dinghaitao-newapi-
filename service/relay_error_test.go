package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var relayCircuitTestChannels atomic.Int64

func withRelayCircuitTestSettings(t *testing.T, useRedis bool) *miniredis.Miniredis {
	t.Helper()
	oldEnabled, oldRDB := common.RedisEnabled, common.RDB
	oldThreshold, oldForbidden, oldInternal := common.ChannelCircuitFailureThreshold, common.ChannelCircuitForbiddenThreshold, common.ChannelCircuitInternalThreshold
	oldWindow, oldCooldown := common.ChannelCircuitFailureWindow, common.ChannelCircuitCooldown
	common.RedisEnabled = useRedis
	common.ChannelCircuitFailureThreshold, common.ChannelCircuitForbiddenThreshold, common.ChannelCircuitInternalThreshold = 2, 2, 2
	common.ChannelCircuitFailureWindow, common.ChannelCircuitCooldown = time.Minute, time.Minute
	var server *miniredis.Miniredis
	if useRedis {
		server = miniredis.RunT(t)
		client := redis.NewClient(&redis.Options{Addr: server.Addr()})
		common.RDB = client
		t.Cleanup(func() { require.NoError(t, client.Close()) })
	}
	t.Cleanup(func() {
		common.RedisEnabled, common.RDB = oldEnabled, oldRDB
		common.ChannelCircuitFailureThreshold, common.ChannelCircuitForbiddenThreshold, common.ChannelCircuitInternalThreshold = oldThreshold, oldForbidden, oldInternal
		common.ChannelCircuitFailureWindow, common.ChannelCircuitCooldown = oldWindow, oldCooldown
	})
	return server
}

func TestRecordRelayChannelCircuitFailurePreservesHealthForLocalErrors(t *testing.T) {
	for _, useRedis := range []bool{false, true} {
		name := "memory"
		if useRedis {
			name = "redis"
		}
		t.Run(name, func(t *testing.T) {
			server := withRelayCircuitTestSettings(t, useRedis)
			channelID := 730000 + int(relayCircuitTestChannels.Add(1))
			modelName := t.Name()
			localErrors := []*types.NewAPIError{nil}
			for _, code := range []types.ErrorCode{
				types.ErrorCodeInvalidRequest, types.ErrorCodeBadRequestBody,
				types.ErrorCodeReadRequestBodyFailed, types.ErrorCodeConvertRequestFailed,
				types.ErrorCodeChannelModelMappedError, types.ErrorCodeChannelParamOverrideInvalid,
				types.ErrorCodeChannelHeaderOverrideInvalid, types.ErrorCodeCountTokenFailed,
				types.ErrorCodeModelPriceError, types.ErrorCodeInvalidApiType,
				types.ErrorCodeJsonMarshalFailed, types.ErrorCodeGenRelayInfoFailed,
			} {
				localErrors = append(localErrors, types.NewError(errors.New("local request rejection"), code))
			}
			localErrors = append(localErrors,
				types.NewErrorWithStatusCode(errors.New("invalid request"), types.ErrorCodeInvalidRequest, http.StatusBadRequest),
				relaycommon.NewAPIErrorFromParamOverride(&relaycommon.ParamOverrideReturnError{
					Message: " local policy rejection ", StatusCode: http.StatusServiceUnavailable,
					Code: "provider_like_error", Type: "server_error", SkipRetry: true,
				}),
			)
			policyErr := localErrors[len(localErrors)-1]
			assert.Equal(t, "local policy rejection", policyErr.Error())
			assert.Equal(t, http.StatusServiceUnavailable, policyErr.StatusCode)
			assert.Equal(t, "server_error", policyErr.ToOpenAIError().Type)
			assert.Equal(t, "provider_like_error", policyErr.ToOpenAIError().Code)
			assert.True(t, types.IsSkipRetryError(policyErr))
			for range 3 {
				for _, apiErr := range localErrors {
					count, tripped := RecordRelayChannelCircuitFailure(channelID, modelName, apiErr)
					assert.Zero(t, count)
					assert.False(t, tripped)
				}
			}
			assert.Equal(t, []int{channelID}, common.FilterUsableChannelIDs([]int{channelID}, modelName))

			upstreamErr := types.NewOpenAIError(errors.New("gateway failure"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
			count, tripped := RecordRelayChannelCircuitFailure(channelID, modelName, upstreamErr)
			require.Equal(t, 1, count)
			require.False(t, tripped)
			for _, apiErr := range localErrors {
				_, _ = RecordRelayChannelCircuitFailure(channelID, modelName, apiErr)
			}
			count, tripped = RecordRelayChannelCircuitFailure(channelID, modelName, upstreamErr)
			require.Equal(t, 2, count, "local errors must not reset real failures")
			require.True(t, tripped)
			if server != nil {
				server.FastForward(30 * time.Second)
			}
			for _, apiErr := range localErrors {
				_, _ = RecordRelayChannelCircuitFailure(channelID, modelName, apiErr)
			}
			assert.True(t, common.IsChannelCircuitOpen(channelID, modelName))
			if server != nil {
				server.FastForward(31 * time.Second)
				assert.False(t, common.IsChannelCircuitOpen(channelID, modelName), "local errors must not extend cooldown")
			}
		})
	}
}

func TestRecordRelayChannelCircuitFailureKeepsUpstreamProtection(t *testing.T) {
	for _, useRedis := range []bool{false, true} {
		name := "memory"
		if useRedis {
			name = "redis"
		}
		t.Run(name, func(t *testing.T) {
			withRelayCircuitTestSettings(t, useRedis)
			errs := []*types.NewAPIError{
				types.NewError(errors.New("upstream connection failed"), types.ErrorCodeDoRequestFailed),
				types.NewError(errors.New("upstream response truncated"), types.ErrorCodeReadResponseBodyFailed),
				types.NewOpenAIError(errors.New("upstream failed"), types.ErrorCodeConvertRequestFailed, http.StatusInternalServerError),
			}
			for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
				errs = append(errs, types.NewOpenAIError(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, status))
			}
			for _, apiErr := range errs {
				channelID := 730000 + int(relayCircuitTestChannels.Add(1))
				count, tripped := RecordRelayChannelCircuitFailure(channelID, t.Name(), apiErr)
				require.Equal(t, 1, count)
				require.False(t, tripped)
				count, tripped = RecordRelayChannelCircuitFailure(channelID, t.Name(), apiErr)
				assert.Equal(t, 2, count)
				assert.True(t, tripped)
				assert.True(t, common.IsChannelCircuitOpen(channelID, t.Name()))
			}
		})
	}
}

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

func TestInputSizeErrorsPreserve413AndNeverRetry(t *testing.T) {
	previousRetryRanges := operation_setting.AutomaticRetryStatusCodeRanges
	t.Cleanup(func() { operation_setting.AutomaticRetryStatusCodeRanges = previousRetryRanges })

	for _, policy := range []struct {
		name   string
		ranges []operation_setting.StatusCodeRange
	}{
		{name: "default", ranges: previousRetryRanges},
		{name: "gateway only", ranges: []operation_setting.StatusCodeRange{{Start: 502, End: 502}}},
		{name: "all errors", ranges: []operation_setting.StatusCodeRange{{Start: 400, End: 599}}},
	} {
		t.Run(policy.name, func(t *testing.T) {
			operation_setting.AutomaticRetryStatusCodeRanges = policy.ranges
			assert.False(t, operation_setting.ShouldRetryByStatusCode(http.StatusRequestEntityTooLarge))
			for _, tc := range []struct {
				name, code, message, mapping string
				upstreamStatus, finalStatus  int
				wantRetry                    bool
			}{
				{name: "input code on 400", code: "input_too_large", message: "Input exceeds the image service limit.", upstreamStatus: 400, finalStatus: 413},
				{name: "upstream code on 502", code: "upstream_request_too_large", message: "Image generation failed.", upstreamStatus: 502, finalStatus: 413},
				{name: "input code cannot map to 502", code: "input_too_large", message: "Input exceeds the image service limit.", upstreamStatus: 400, finalStatus: 413, mapping: `{"400":502,"413":502}`},
				{name: "upstream code cannot map to 502", code: "upstream_request_too_large", message: "Image generation failed.", upstreamStatus: 502, finalStatus: 413, mapping: `{"502":502,"413":502}`},
				{name: "actual 413 cannot map to 502", code: "upstream_error", message: "Payload rejected.", upstreamStatus: 413, finalStatus: 413, mapping: `{"413":502}`},
				{name: "ordinary 502", code: "upstream_error", message: "Image generation failed.", upstreamStatus: 502, finalStatus: 502, wantRetry: true},
				{name: "size words alone are not an input code", code: "upstream_error", message: "input_too_large: request too large", upstreamStatus: 502, finalStatus: 502, wantRetry: true},
				{name: "context error is unchanged", code: "context_length_exceeded", message: "Maximum context length exceeded.", upstreamStatus: 502, finalStatus: 502, wantRetry: true},
				{name: "ordinary status mapping is unchanged", code: "upstream_error", message: "Other request error.", upstreamStatus: 400, finalStatus: 502, mapping: `{"400":502}`, wantRetry: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					for _, mode := range []string{"sync", "async"} {
						t.Run(mode, func(t *testing.T) {
							c, _ := gin.CreateTestContext(httptest.NewRecorder())
							c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", nil)
							if mode == "async" {
								c.Set(ImageAsyncTaskIDContextKey, "imgtask_test")
							}
							body, err := common.Marshal(gin.H{"error": types.OpenAIError{Code: tc.code, Message: tc.message, Type: "upstream_error"}})
							require.NoError(t, err)
							apiErr := RelayErrorHandler(c.Request.Context(), &http.Response{StatusCode: tc.upstreamStatus, Body: io.NopCloser(bytes.NewReader(body))}, false)
							require.NotNil(t, apiErr)
							if tc.finalStatus == http.StatusRequestEntityTooLarge {
								assert.Equal(t, http.StatusRequestEntityTooLarge, apiErr.StatusCode, "normalize before channel mappings")
							}
							ResetStatusCode(apiErr, tc.mapping)
							decision := DecideRelayRetry(c, apiErr, 2)
							if tc.wantRetry {
								assert.Equal(t, PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}, decision)
							} else {
								assert.Equal(t, PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}, decision)
							}
							assert.Equal(t, tc.finalStatus, apiErr.StatusCode)
							assert.Equal(t, types.ErrorCode(tc.code), apiErr.GetErrorCode())
							assert.Equal(t, tc.message, apiErr.Error())
							assert.Equal(t, tc.code, apiErr.ToOpenAIError().Code)
							assert.Equal(t, tc.message, apiErr.ToOpenAIError().Message)
						})
					}
				})
			}
		})
	}
	for _, code := range []types.ErrorCode{"input_too_large", "upstream_request_too_large"} {
		t.Run("direct error "+string(code), func(t *testing.T) {
			apiErr := types.NewOpenAIError(errors.New("Image generation failed."), code, http.StatusBadGateway)
			assert.Equal(t, "stop", DecideRelayRetry(nil, apiErr, 2).Action)
			assert.Equal(t, http.StatusRequestEntityTooLarge, apiErr.StatusCode)
			// Image adapters may find an error inside a successful HTTP body and
			// skip retry before reaching the shared decision helper.
			apiErr = types.NewOpenAIError(errors.New("Image generation failed."), code, http.StatusOK, types.ErrOptionWithSkipRetry())
			ResetStatusCode(apiErr, "")
			assert.Equal(t, http.StatusRequestEntityTooLarge, apiErr.StatusCode)
		})
	}
}
