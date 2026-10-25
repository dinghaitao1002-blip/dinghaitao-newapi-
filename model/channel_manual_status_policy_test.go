package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
)

func TestAutomaticDisableRejectedBeforeStateAccess(t *testing.T) {
	// This boundary must reject automatic writes even without an initialized
	// database or cache, for both single-key and multi-key callers.
	for _, key := range []string{"", "test-key"} {
		assert.False(t, UpdateChannelStatus(99999, key, common.ChannelStatusAutoDisabled, "upstream timeout"))
	}
}
