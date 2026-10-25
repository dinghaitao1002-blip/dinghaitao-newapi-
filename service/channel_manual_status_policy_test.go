package service

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
)

func TestAutomaticDisablePolicyCannotBeEnabledByLegacySetting(t *testing.T) {
	previous := common.AutomaticDisableChannelEnabled
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previous })
	common.AutomaticDisableChannelEnabled = true
	assert.False(t, ShouldDisableChannel(nil))
	for _, status := range []int{400, 401, 403, 429, 500, 502, 503, 504} {
		err := types.NewOpenAIError(errors.New("upstream timeout"), types.ErrorCodeChannelResponseTimeExceeded, status)
		assert.False(t, ShouldDisableChannel(err), "status %d", status)
	}
}
