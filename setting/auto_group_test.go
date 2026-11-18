package setting

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveModelConcurrencyRuleUsesRoutingIdentity(t *testing.T) {
	const base = "gemini-3-pro-image-preview"
	const modified = base + "@temperature:0.2"
	for _, test := range []struct {
		name         string
		enabled      bool
		raw          string
		exactEnabled bool
		wantModel    string
		wantLimit    int
	}{
		{name: "base", enabled: true, raw: base, wantModel: base, wantLimit: 2},
		{name: "modifier fallback", enabled: true, raw: base + "@temperature:0.8", wantModel: base, wantLimit: 2},
		{name: "enabled exact wins", enabled: true, raw: modified, exactEnabled: true, wantModel: modified, wantLimit: 5},
		{name: "disabled exact cannot bypass base", enabled: true, raw: modified, wantModel: base, wantLimit: 2},
		{name: "global disabled", raw: base},
		{name: "unconfigured model", enabled: true, raw: "another-image"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := common.ModelConcurrencyConfig{Enabled: test.enabled, Rules: []common.ModelConcurrencyRule{
				{Model: base, Enabled: true, MaxConcurrent: 2},
				{Model: modified, Enabled: test.exactEnabled, MaxConcurrent: 5},
			}}
			rule, matched := ResolveModelConcurrencyRule(config, test.raw)
			assert.Equal(t, test.wantModel != "", matched)
			assert.Equal(t, test.wantModel, rule.Model)
			assert.Equal(t, test.wantLimit, rule.MaxConcurrent)
		})
	}
}

func TestUpdateMaxTokenAutoGroupsAcceptsAnyPositiveInteger(t *testing.T) {
	original := GetMaxTokenAutoGroups()
	t.Cleanup(func() {
		require.NoError(t, UpdateMaxTokenAutoGroups(fmt.Sprintf("%d", original)))
	})

	require.NoError(t, UpdateMaxTokenAutoGroups("123456"))
	assert.Equal(t, 123456, GetMaxTokenAutoGroups())
}

func TestUpdateMaxTokenAutoGroupsRejectsInvalidValuesWithoutChangingState(t *testing.T) {
	original := GetMaxTokenAutoGroups()
	for _, value := range []string{"", "0", "-1", "1.5", "not-a-number"} {
		t.Run(value, func(t *testing.T) {
			assert.Error(t, UpdateMaxTokenAutoGroups(value))
			assert.Equal(t, original, GetMaxTokenAutoGroups())
		})
	}
}
