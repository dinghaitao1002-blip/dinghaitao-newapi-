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

func TestResolveModelConcurrencyRulePrefersGroupBeforeGlobalAliases(t *testing.T) {
	const base = "gemini-3-pro-image-preview"
	config := common.ModelConcurrencyConfig{Enabled: true, Rules: []common.ModelConcurrencyRule{
		{Model: base, Enabled: true, MaxConcurrent: 10},
		{Model: base + "@temperature:0.2", Enabled: true, MaxConcurrent: 20},
		{Group: "vip", Model: base, Enabled: true, MaxConcurrent: 3},
		{Group: "vip", Model: base + "@temperature:0.2", Enabled: true, MaxConcurrent: 4},
	}}
	for _, tc := range []struct {
		group, raw string
		index      int
	}{
		{"vip", base, 2}, {"vip", base + "@temperature:0.8", 2}, {"vip", base + "@temperature:0.2", 3},
		{"standard", base, 0}, {"standard", base + "@temperature:0.2", 1},
	} {
		rule, ok := ResolveModelConcurrencyRule(config, tc.raw, tc.group)
		require.True(t, ok)
		assert.Equal(t, config.Rules[tc.index], rule)
	}
	config.Rules[2].Enabled = false
	config.Rules[3].Enabled = false
	rule, ok := ResolveModelConcurrencyRule(config, base, "vip")
	require.True(t, ok)
	assert.Equal(t, "", rule.Group)
	assert.Equal(t, 10, rule.MaxConcurrent)
}
