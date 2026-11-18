package setting

import (
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

var modelConcurrencyConfig atomic.Pointer[common.ModelConcurrencyConfig]

// ResolveModelConcurrencyRule uses the same modifier/legacy-alias fallback as
// channel routing. An enabled exact rule has its own bucket. A disabled exact
// entry is not an exemption from an enabled rule for the underlying model.
func ResolveModelConcurrencyRule(config common.ModelConcurrencyConfig, raw string) (common.ModelConcurrencyRule, bool) {
	if rule, enabled := config.Rule(raw); enabled {
		return rule, true
	}
	return config.Rule(ratio_setting.RoutingMatchModelName(raw))
}

func GetModelConcurrencyConfig() common.ModelConcurrencyConfig {
	config := modelConcurrencyConfig.Load()
	if config == nil {
		return common.ModelConcurrencyConfig{Rules: []common.ModelConcurrencyRule{}}
	}
	// Callers receive an independent snapshot, including nested user overrides.
	copy := *config
	copy.Rules = append([]common.ModelConcurrencyRule{}, config.Rules...)
	for i := range copy.Rules {
		copy.Rules[i].UserLimits = append([]common.ModelConcurrencyUserLimit{}, copy.Rules[i].UserLimits...)
	}
	return copy
}

func UpdateModelConcurrencyConfigByJSONString(raw string) error {
	config, err := common.ParseModelConcurrencyConfig(raw)
	if err != nil {
		return err
	}
	modelConcurrencyConfig.Store(&config)
	return nil
}

func ModelConcurrencyConfig2JSONString() string {
	value, _ := common.Marshal(GetModelConcurrencyConfig())
	return string(value)
}
