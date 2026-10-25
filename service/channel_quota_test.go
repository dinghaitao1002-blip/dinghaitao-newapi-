package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
)

func TestChannelQuotaExhaustionMatchesOnly92UpstreamBalanceError(t *testing.T) {
	upstream := RelayErrorHandler(context.Background(), &http.Response{
		StatusCode: 402,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"user quota is not enough","type":"new_api_error","code":"insufficient_user_quota"}}`)),
	}, false)
	assert.True(t, IsChannelQuotaExhausted(92, upstream))
	assert.False(t, IsChannelQuotaExhausted(97, upstream))
	assert.False(t, IsChannelQuotaExhausted(92, nil))
	assert.False(t, IsChannelQuotaExhausted(92, types.NewErrorWithStatusCode(
		errors.New("user quota is not enough"), types.ErrorCodeInsufficientUserQuota, 402)))
	assert.False(t, IsChannelQuotaExhausted(92, types.NewOpenAIError(
		errors.New("user quota is not enough"), types.ErrorCodeBadResponseStatusCode, 500)))
	assert.False(t, IsChannelQuotaExhausted(92, types.NewOpenAIError(
		errors.New("payment required for requested feature"), types.ErrorCodeBadResponseStatusCode, 402)))
	assert.False(t, ShouldEnableChannel(nil, 2))
}
