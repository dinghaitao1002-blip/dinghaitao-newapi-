package ratio_setting

import (
	"math"
	"time"

	"github.com/QuantumNous/new-api/types"
)

// GroupRatioSchedule scales a group's saved base/special ratios in two daily
// windows. It does not alter channel routing, status, model prices or database
// options when the clock crosses a boundary. Zero/invalid schedules are ignored.
type GroupRatioSchedule struct {
	Enabled         bool    `json:"enabled"`
	DayStartMinute  int     `json:"day_start_minute"`
	DayEndMinute    int     `json:"day_end_minute"`
	DayMultiplier   float64 `json:"day_multiplier"`
	NightMultiplier float64 `json:"night_multiplier"`
}

var groupRatioScheduleMap = types.NewRWMap[string, GroupRatioSchedule]()
var groupPricingBeijing = time.FixedZone("Asia/Shanghai", 8*60*60)

func applyGroupRatioSchedule(group string, base float64, at time.Time) float64 {
	schedule, ok := groupRatioScheduleMap.Get(group)
	if !ok || !schedule.Enabled {
		return base
	}
	if schedule.DayStartMinute < 0 || schedule.DayStartMinute >= 1440 ||
		schedule.DayEndMinute < 0 || schedule.DayEndMinute >= 1440 ||
		schedule.DayStartMinute == schedule.DayEndMinute ||
		!validGroupScheduleMultiplier(schedule.DayMultiplier) ||
		!validGroupScheduleMultiplier(schedule.NightMultiplier) {
		return base
	}
	if at.IsZero() {
		at = time.Now()
	}
	local := at.In(groupPricingBeijing)
	minute := local.Hour()*60 + local.Minute()
	day := minute >= schedule.DayStartMinute && minute < schedule.DayEndMinute
	if schedule.DayStartMinute > schedule.DayEndMinute {
		day = minute >= schedule.DayStartMinute || minute < schedule.DayEndMinute
	}
	multiplier := schedule.NightMultiplier
	if day {
		multiplier = schedule.DayMultiplier
	}
	result := base * multiplier
	if math.IsNaN(result) || math.IsInf(result, 0) || result < 0 {
		return base
	}
	return result
}

func validGroupScheduleMultiplier(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
