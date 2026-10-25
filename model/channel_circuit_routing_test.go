package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRequestsAvoidOpenCircuitWithoutDisablingChannel(t *testing.T) {
	oldMemory, oldRedis := common.MemoryCacheEnabled, common.RedisEnabled
	oldThreshold := common.ChannelCircuitFailureThreshold
	oldGroups, oldChannels := group2model2channels, channelsIDM
	common.MemoryCacheEnabled, common.RedisEnabled = true, false
	common.ChannelCircuitFailureThreshold = 10
	t.Cleanup(func() {
		common.MemoryCacheEnabled, common.RedisEnabled = oldMemory, oldRedis
		common.ChannelCircuitFailureThreshold = oldThreshold
		group2model2channels, channelsIDM = oldGroups, oldChannels
	})
	const modelName = "routing-circuit-regression"
	first := &Channel{Id: 900046, Status: common.ChannelStatusEnabled, Priority: common.GetPointer(int64(10)), Weight: common.GetPointer(uint(100))}
	backup := &Channel{Id: 900090, Status: common.ChannelStatusEnabled, Priority: common.GetPointer(int64(0)), Weight: common.GetPointer(uint(100))}
	channelsIDM = map[int]*Channel{first.Id: first, backup.Id: backup}
	group2model2channels = map[string]map[string][]int{
		"same-group":     {modelName: {first.Id, backup.Id}},
		"single-channel": {modelName: {first.Id}},
	}
	selected, err := GetRandomSatisfiedChannel("same-group", modelName, 0, nil)
	require.NoError(t, err)
	require.Equal(t, first.Id, selected.Id)
	for n := 1; n <= 10; n++ {
		_, tripped := common.RecordChannelCircuitFailure(first.Id, modelName, 504, "upstream timeout")
		assert.Equal(t, n == 10, tripped)
	}
	// This is an entirely new request (retry=0), not retrying the old request.
	selected, err = GetRandomSatisfiedChannel("same-group", modelName, 0, nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, backup.Id, selected.Id)
	common.ResetChannelCircuitOnSuccess(first.Id, modelName)
	selected, err = GetRandomSatisfiedChannel("same-group", modelName, 0, nil)
	require.NoError(t, err)
	assert.Equal(t, backup.Id, selected.Id)
	assert.Equal(t, common.ChannelStatusEnabled, first.Status)
	selected, err = GetRandomSatisfiedChannel("single-channel", modelName, 0, nil)
	require.NoError(t, err)
	assert.Nil(t, selected) // Must not silently borrow a channel from another group.
}

func TestDatabaseRoutingUsesHealthyLowerPriorityDuringCircuitCooldown(t *testing.T) {
	truncateTables(t)
	oldMemory, oldRedis := common.MemoryCacheEnabled, common.RedisEnabled
	oldThreshold := common.ChannelCircuitFailureThreshold
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	common.ChannelCircuitFailureThreshold = 1
	t.Cleanup(func() {
		common.MemoryCacheEnabled, common.RedisEnabled = oldMemory, oldRedis
		common.ChannelCircuitFailureThreshold = oldThreshold
	})
	const modelName = "database-circuit-upgrade"
	primary := Channel{Id: 900146, Status: common.ChannelStatusEnabled, Priority: common.GetPointer(int64(10))}
	backup := Channel{Id: 900190, Status: common.ChannelStatusEnabled, Priority: common.GetPointer(int64(0))}
	require.NoError(t, DB.Create(&primary).Error)
	require.NoError(t, DB.Create(&backup).Error)
	for _, channel := range []Channel{primary, backup} {
		require.NoError(t, DB.Create(&Ability{Group: "upgrade-test", Model: modelName, ChannelId: channel.Id, Enabled: true, Priority: channel.Priority}).Error)
	}
	selected, err := GetRandomSatisfiedChannel("upgrade-test", modelName, 0, nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, primary.Id, selected.Id)
	_, tripped := common.RecordChannelCircuitFailure(primary.Id, modelName, 504, "upstream timeout")
	require.True(t, tripped)
	selected, err = GetRandomSatisfiedChannel("upgrade-test", modelName, 0, nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, backup.Id, selected.Id)
	require.NoError(t, DB.First(&primary, primary.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, primary.Status)
	selected, err = GetRandomSatisfiedChannel("other-group", modelName, 0, nil)
	require.NoError(t, err)
	assert.Nil(t, selected)
}
