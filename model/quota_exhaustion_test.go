package model

import (
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These tests replace package globals and must not run in parallel. Every row,
// key and response is synthetic; no production database or upstream is used.
func setupQuotaExhaustionDB(t *testing.T, memoryCache bool) string {
	t.Helper()
	oldDB := DB
	oldDBType := common.MainDatabaseType()
	oldMemory, oldRedis := common.MemoryCacheEnabled, common.RedisEnabled
	oldGroups, oldChannels := group2model2channels, channelsIDM
	oldAdvanced := channel2advancedCustomConfig
	oldGroupCol, oldKeyCol := commonGroupCol, commonKeyCol
	oldTrueVal, oldFalseVal := commonTrueVal, commonFalseVal
	oldLogGroupCol, oldLogKeyCol := logGroupCol, logKeyCol

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "quota-test.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		DB = oldDB
		common.SetMainDatabaseType(oldDBType)
		common.MemoryCacheEnabled, common.RedisEnabled = oldMemory, oldRedis
		group2model2channels, channelsIDM = oldGroups, oldChannels
		channel2advancedCustomConfig = oldAdvanced
		commonGroupCol, commonKeyCol = oldGroupCol, oldKeyCol
		commonTrueVal, commonFalseVal = oldTrueVal, oldFalseVal
		logGroupCol, logKeyCol = oldLogGroupCol, oldLogKeyCol
		InvalidatePricingCache()
		assert.NoError(t, sqlDB.Close())
	})

	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled, common.RedisEnabled = memoryCache, false
	group2model2channels, channelsIDM, channel2advancedCustomConfig = nil, nil, nil
	initCol()
	require.NoError(t, DB.AutoMigrate(&Channel{}, &Ability{}))

	modelName := "quota-exhaustion-" + t.Name()
	channels := []*Channel{
		{
			Id: 92, Type: 1, Key: "synthetic-key-92", Name: "quota-primary",
			Status: common.ChannelStatusEnabled, Category: "banana-test",
			Group: "quota-shared,quota-exclusive", Models: modelName + ",quota-second-model",
			Priority: common.GetPointer(int64(100)), Weight: common.GetPointer(uint(100)),
			UsedQuota: 123, OtherInfo: `{"custom":"preserve","nested":{"mode":"test"},"status_reason":"previous note"}`,
		},
		{
			Id: 97, Type: 1, Key: "synthetic-key-97", Name: "quota-backup",
			Status: common.ChannelStatusEnabled, Category: "banana-test",
			Group: "quota-shared", Models: modelName,
			Priority: common.GetPointer(int64(10)), Weight: common.GetPointer(uint(100)),
		},
		{
			// Same model and category, but another group: never a valid fallback.
			Id: 98, Type: 1, Key: "synthetic-key-98", Name: "wrong-group",
			Status: common.ChannelStatusEnabled, Category: "banana-test",
			Group: "quota-foreign", Models: modelName,
			Priority: common.GetPointer(int64(200)), Weight: common.GetPointer(uint(100)),
		},
	}
	for _, channel := range channels {
		require.NoError(t, DB.Create(channel).Error)
		require.NoError(t, channel.AddAbilities(DB))
	}
	InitChannelCache()
	return modelName
}

func requireQuotaChannelState(t *testing.T, channelID, status int, abilitiesEnabled bool) Channel {
	t.Helper()
	var channel Channel
	require.NoError(t, DB.First(&channel, channelID).Error)
	require.Equal(t, status, channel.Status)
	var abilities []Ability
	require.NoError(t, DB.Where("channel_id = ?", channelID).Find(&abilities).Error)
	require.NotEmpty(t, abilities)
	for _, ability := range abilities {
		assert.Equal(t, abilitiesEnabled, ability.Enabled, "group=%s model=%s", ability.Group, ability.Model)
	}
	if common.MemoryCacheEnabled {
		cached, err := CacheGetChannel(channelID)
		require.NoError(t, err)
		require.NotNil(t, cached)
		assert.Equal(t, status, cached.Status)
	}
	return channel
}

func requireQuotaSelection(t *testing.T, group, model string, retry, wantID int) {
	t.Helper()
	selected, err := GetRandomSatisfiedChannel(group, model, retry, []dto.ChannelFilter{{Kind: dto.FilterRequestPath, RequestPath: "/v1/images/generations"}})
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, wantID, selected.Id)
}

func TestStopChannelForQuotaExhaustionRoutesNewRequestsAndRetries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		memory bool
	}{
		{"memory_cache", true},
		{"database_routing", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			modelName := setupQuotaExhaustionDB(t, tc.memory)
			requireQuotaSelection(t, "quota-shared", modelName, 0, 92)
			requireQuotaSelection(t, "quota-exclusive", modelName, 0, 92)

			stopped, err := StopChannelForQuotaExhaustion(92)
			require.NoError(t, err)
			require.True(t, stopped)
			requireQuotaChannelState(t, 92, common.ChannelStatusManuallyDisabled, false)
			requireQuotaChannelState(t, 97, common.ChannelStatusEnabled, true)
			requireQuotaChannelState(t, 98, common.ChannelStatusEnabled, true)

			// retry=0 proves a new request avoids 92 immediately, without a sync.
			// Positive retries prove the failed request can select the backup too.
			for _, retry := range []int{0, 1, 3} {
				requireQuotaSelection(t, "quota-shared", modelName, retry, 97)
			}
			selected, err := GetRandomSatisfiedChannel("quota-exclusive", modelName, 0, []dto.ChannelFilter{{Kind: dto.FilterRequestPath, RequestPath: "/v1/images/generations"}})
			require.NoError(t, err)
			require.Nil(t, selected, "must not borrow 97 or 98 from another group")
			selected, _ = GetRandomSatisfiedChannel("quota-exclusive", modelName, 1, []dto.ChannelFilter{{Kind: dto.FilterRequestPath, RequestPath: "/v1/images/generations"}})
			require.Nil(t, selected, "retry must not escape the original group")
			selected, err = GetRandomSatisfiedChannel("quota-shared", "quota-second-model", 0, []dto.ChannelFilter{{Kind: dto.FilterRequestPath, RequestPath: "/v1/images/generations"}})
			require.NoError(t, err)
			require.Nil(t, selected, "a backup without the requested model must not be selected")
		})
	}
}

func TestStopChannelForQuotaExhaustionSurvivesLateSuccessAndCacheReload(t *testing.T) {
	modelName := setupQuotaExhaustionDB(t, true)
	stopped, err := StopChannelForQuotaExhaustion(92)
	require.NoError(t, err)
	require.True(t, stopped)

	// A success from an already in-flight request can reset the transient
	// circuit state, but cannot undo this persisted channel disable.
	common.ResetChannelCircuitOnSuccess(92, modelName)
	requireQuotaSelection(t, "quota-shared", modelName, 0, 97)
	for i := 0; i < 2; i++ {
		InitChannelCache()
		common.ResetChannelCircuitOnSuccess(92, modelName)
		requireQuotaChannelState(t, 92, common.ChannelStatusManuallyDisabled, false)
		requireQuotaSelection(t, "quota-shared", modelName, 0, 97)
	}
}

func TestStopChannelForQuotaExhaustionPreservesMetadataAndRepeatedReason(t *testing.T) {
	setupQuotaExhaustionDB(t, true)
	stopped, err := StopChannelForQuotaExhaustion(92)
	require.NoError(t, err)
	require.True(t, stopped)
	channel := requireQuotaChannelState(t, 92, common.ChannelStatusManuallyDisabled, false)
	info := channel.GetOtherInfo()
	assert.Equal(t, "preserve", info["custom"])
	assert.Equal(t, map[string]interface{}{"mode": "test"}, info["nested"])
	require.NotEmpty(t, info["status_reason"])
	require.NotEqual(t, "previous note", info["status_reason"])
	require.Contains(t, info, "status_time")
	assert.Equal(t, int64(123), channel.UsedQuota)
	assert.Equal(t, "synthetic-key-92", channel.Key)
	assert.Equal(t, "quota-primary", channel.Name)
	assert.Equal(t, int64(100), channel.GetPriority())

	// Compare the complete JSON string so a repeated event cannot silently
	// replace the first reason, timestamp, or unrelated metadata.
	firstOtherInfo := channel.OtherInfo
	stopped, err = StopChannelForQuotaExhaustion(92)
	require.NoError(t, err)
	require.True(t, stopped, "already disabled is a successful idempotent stop")
	channel = requireQuotaChannelState(t, 92, common.ChannelStatusManuallyDisabled, false)
	assert.Equal(t, firstOtherInfo, channel.OtherInfo)
}

func TestStopChannelForQuotaExhaustionPreservesExistingManualReason(t *testing.T) {
	modelName := setupQuotaExhaustionDB(t, true)
	const manualInfo = `{"status_reason":"manual maintenance","status_time":123,"custom":"keep"}`
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 92).Updates(map[string]interface{}{
		"status": common.ChannelStatusManuallyDisabled, "other_info": manualInfo,
	}).Error)
	// Deliberately leave the previous abilities/cache enabled. An idempotent
	// call must also repair these stale derived states, without changing reason.
	stopped, err := StopChannelForQuotaExhaustion(92)
	require.NoError(t, err)
	require.True(t, stopped)
	channel := requireQuotaChannelState(t, 92, common.ChannelStatusManuallyDisabled, false)
	assert.Equal(t, manualInfo, channel.OtherInfo)
	requireQuotaSelection(t, "quota-shared", modelName, 0, 97)
}

func TestStopChannelForQuotaExhaustionAbilityFailureRollsBackStatusAndCache(t *testing.T) {
	modelName := setupQuotaExhaustionDB(t, true)
	before := requireQuotaChannelState(t, 92, common.ChannelStatusEnabled, true)
	// A real SQLite trigger fails the second table write, after the channel
	// update has been attempted. This exercises the transaction, not a mock.
	require.NoError(t, DB.Exec(`
		CREATE TRIGGER reject_quota_ability_update
		BEFORE UPDATE OF enabled ON abilities
		WHEN OLD.channel_id = 92
		BEGIN
			SELECT RAISE(ABORT, 'synthetic ability update failure');
		END;
	`).Error)
	stopped, err := StopChannelForQuotaExhaustion(92)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "synthetic ability update failure")
	assert.False(t, stopped)
	after := requireQuotaChannelState(t, 92, common.ChannelStatusEnabled, true)
	assert.Equal(t, before.OtherInfo, after.OtherInfo)
	requireQuotaSelection(t, "quota-shared", modelName, 0, 92)
	InitChannelCache()
	requireQuotaChannelState(t, 92, common.ChannelStatusEnabled, true)
	requireQuotaSelection(t, "quota-shared", modelName, 0, 92)

	// After removing the injected failure, the exact same operation succeeds.
	require.NoError(t, DB.Exec("DROP TRIGGER reject_quota_ability_update").Error)
	stopped, err = StopChannelForQuotaExhaustion(92)
	require.NoError(t, err)
	require.True(t, stopped)
	requireQuotaChannelState(t, 92, common.ChannelStatusManuallyDisabled, false)
	requireQuotaSelection(t, "quota-shared", modelName, 0, 97)
}

func TestStopChannelForQuotaExhaustionConvertsRecoverableDisableToManual(t *testing.T) {
	modelName := setupQuotaExhaustionDB(t, true)
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 92).Update("status", common.ChannelStatusAutoDisabled).Error)
	stopped, err := StopChannelForQuotaExhaustion(92)
	require.NoError(t, err)
	require.True(t, stopped)
	requireQuotaChannelState(t, 92, common.ChannelStatusManuallyDisabled, false)
	InitChannelCache()
	requireQuotaSelection(t, "quota-shared", modelName, 0, 97)
}
