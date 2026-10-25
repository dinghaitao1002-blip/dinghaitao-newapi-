package ratio_setting

import (
	"math"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduledGroupRatiosBeijingBoundariesAndIsolation(t *testing.T) {
	saved := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"group_ratio_setting.group_ratio":           `{"1k早八晚八分组":1,"other":1.7}`,
		"group_ratio_setting.group_group_ratio":     `{"vip":{"1k早八晚八分组":0.9,"other":0.8}}`,
		"group_ratio_setting.group_ratio_schedules": `{"1k早八晚八分组":{"enabled":true,"day_start_minute":480,"day_end_minute":1200,"day_multiplier":1,"night_multiplier":0.2857142857142857}}`,
	}))
	baseJSON := GroupRatio2JSONString()
	for _, tc := range []struct {
		at    string
		want  float64
		quota int
	}{
		{"2026-09-16T23:59:59Z", 2.0 / 7, 1000},
		{"2026-09-17T00:00:00Z", 1, 3500},
		{"2026-09-17T11:59:59Z", 1, 3500},
		{"2026-09-17T12:00:00Z", 2.0 / 7, 1000},
		{"2026-09-17T16:00:00Z", 2.0 / 7, 1000},
	} {
		t.Run(tc.at, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, tc.at)
			require.NoError(t, err)
			ratio := GetGroupRatioAt("1k早八晚八分组", at)
			assert.InDelta(t, tc.want, ratio, 1e-14)
			quota, err := common.QuotaFromFloatStrict(0.007 * 500000 * ratio)
			require.NoError(t, err)
			assert.Equal(t, tc.quota, quota)
			assert.Equal(t, 1.7, GetGroupRatioAt("other", at))
			assert.InDelta(t, ratio, GetGroupRatioCopyAt(at)["1k早八晚八分组"], 1e-14)
			special, ok := GetGroupGroupRatioAt("vip", "1k早八晚八分组", at)
			require.True(t, ok)
			assert.InDelta(t, 0.9*tc.want, special, 1e-14)
			other, ok := GetGroupGroupRatioAt("vip", "other", at)
			require.True(t, ok)
			assert.Equal(t, 0.8, other)
		})
	}
	assert.Equal(t, baseJSON, GroupRatio2JSONString(), "clock changes must not mutate stored group prices")
}

func TestInvalidOrDisabledGroupScheduleKeepsExistingPricing(t *testing.T) {
	saved := groupRatioScheduleMap.ReadAll()
	t.Cleanup(func() { groupRatioScheduleMap.Clear(); groupRatioScheduleMap.AddAll(saved) })
	at := time.Date(2026, 9, 17, 22, 0, 0, 0, groupPricingBeijing)
	for _, schedule := range []GroupRatioSchedule{
		{},
		{true, 480, 1200, 1, -1},
		{true, 480, 1200, 1, math.Inf(1)},
		{true, 480, 1200, math.NaN(), 0.5},
		{true, 480, 1200, 1, 0},
		{true, 480, 480, 1, 0.5},
		{true, -1, 1200, 1, 0.5},
		{true, 480, 1440, 1, 0.5},
	} {
		groupRatioScheduleMap.Set("test-invalid", schedule)
		assert.Equal(t, 1.5, applyGroupRatioSchedule("test-invalid", 1.5, at))
	}
}
