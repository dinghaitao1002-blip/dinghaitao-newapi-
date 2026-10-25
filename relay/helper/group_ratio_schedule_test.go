package helper

import (
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleScheduledGroupUsesRequestStartForRetries(t *testing.T) {
	saved := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"group_ratio_setting.group_ratio":           `{"scheduled-test":1,"other":0.8}`,
		"group_ratio_setting.group_group_ratio":     `{}`,
		"group_ratio_setting.group_ratio_schedules": `{"scheduled-test":{"enabled":true,"day_start_minute":480,"day_end_minute":1200,"day_multiplier":1,"night_multiplier":0.2857142857142857}}`,
	}))
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("auto_group", "scheduled-test")
	for _, tc := range []struct {
		start string
		want  float64
	}{
		{"2026-09-17T11:59:59Z", 1},
		{"2026-09-17T12:00:00Z", 2.0 / 7},
		{"2026-09-16T23:59:59Z", 2.0 / 7},
		{"2026-09-17T00:00:00Z", 1},
	} {
		start, err := time.Parse(time.RFC3339, tc.start)
		require.NoError(t, err)
		info := &relaycommon.RelayInfo{UsingGroup: "other", UserGroup: "default", StartTime: start}
		first := HandleGroupRatio(ctx, info)
		assert.InDelta(t, tc.want, first.GroupRatio, 1e-14)
		assert.Equal(t, "scheduled-test", info.UsingGroup)
		assert.Equal(t, first, HandleGroupRatio(ctx, info), "retry retains admission-time price")
	}
}
