package setting

import (
	"bytes"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

type modelConcurrencyState struct {
	Config     common.ModelConcurrencyConfig
	Generation uint64
}

var modelConcurrencyConfig atomic.Pointer[modelConcurrencyState]

// ResolveModelConcurrencyRule uses the same modifier/legacy-alias fallback as
// channel routing. An enabled exact rule has its own bucket. A disabled exact
// entry is not an exemption from an enabled rule for the underlying model.
func ResolveModelConcurrencyRule(config common.ModelConcurrencyConfig, raw string, groups ...string) (common.ModelConcurrencyRule, bool) {
	if len(groups) > 0 && groups[0] != "" {
		if rule, enabled := config.Rule(raw, groups[0]); enabled {
			return rule, true
		}
		if rule, enabled := config.Rule(ratio_setting.RoutingMatchModelName(raw), groups[0]); enabled {
			return rule, true
		}
	}
	if rule, enabled := config.Rule(raw); enabled {
		return rule, true
	}
	return config.Rule(ratio_setting.RoutingMatchModelName(raw))
}

func GetModelConcurrencyConfig() common.ModelConcurrencyConfig {
	config, _ := GetModelConcurrencyConfigSnapshot()
	return config
}

// GetModelConcurrencyConfigSnapshot pairs one immutable rule snapshot with its
// revision, including edit cycles that return to exactly the same JSON.
func GetModelConcurrencyConfigSnapshot() (common.ModelConcurrencyConfig, uint64) {
	state := modelConcurrencyConfig.Load()
	if state == nil {
		return common.ModelConcurrencyConfig{Rules: []common.ModelConcurrencyRule{}}, 0
	}
	copy := state.Config
	copy.Rules = append([]common.ModelConcurrencyRule{}, state.Config.Rules...)
	for i := range copy.Rules {
		copy.Rules[i].UserLimits = append([]common.ModelConcurrencyUserLimit{}, copy.Rules[i].UserLimits...)
	}
	return copy, state.Generation
}

func UpdateModelConcurrencyConfigByJSONString(raw string) error {
	config, err := common.ParseModelConcurrencyConfig(raw)
	if err != nil {
		return err
	}
	encoded, _ := common.Marshal(config)
	for {
		previous := modelConcurrencyConfig.Load()
		next := &modelConcurrencyState{Config: config, Generation: 1}
		if previous != nil {
			canonical, _ := common.Marshal(previous.Config)
			if bytes.Equal(canonical, encoded) {
				return nil
			}
			next.Generation = previous.Generation + 1
		}
		if modelConcurrencyConfig.CompareAndSwap(previous, next) {
			return nil
		}
	}
}

func ModelConcurrencyConfig2JSONString() string {
	value, _ := common.Marshal(GetModelConcurrencyConfig())
	return string(value)
}
