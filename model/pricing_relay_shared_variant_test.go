package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A model declared by a single plugin that relays its unsupported request
// forms (unsupported: "relay") is also served by ordinary channels, so the
// model-level expression must stay a relay expression (fixed()) while the
// plugin needs its own usage expression. The admin editor therefore lists the
// provider variant even though only one plugin declares the model.
func TestPricingRelaySharedSinglePluginListsProviderVariant(t *testing.T) {
	resetPricingEndpointTestTables(t)
	require.NoError(t, DB.AutoMigrate(&Option{}))
	saved := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })

	relaySource := strings.ReplaceAll(pricingUsagePluginSource("1.0.0", `{image_count:{type:"number",unit:"count"}}`), "pricing-usage-probe", "pricing-relay")
	relaySource = strings.Replace(relaySource, `fetchMode: "per_task",`, `fetchMode: "per_task", protocols: [{name: "openai_responses", supports: ["background"], unsupported: "relay"}],`, 1)
	relaySource = strings.Replace(relaySource, `models: ["pricing-usage-model"]`, `models: ["pricing-relay-model"]`, 1)
	relaySource += `
export const protocols = { openai_responses: {
  decodeRequest: function () { return { kind: "submit", model: "pricing-relay-model", action: "image", requestBody: {} }; },
  renderFinal: function () { return { output: [] }; },
} };
`
	_, err := jsplugin.DefaultRegistry.Register(relaySource, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister("pricing-relay") })

	plainSource := strings.ReplaceAll(pricingUsagePluginSource("1.0.0", `{image_count:{type:"number",unit:"count"}}`), "pricing-usage-probe", "pricing-plain")
	plainSource = strings.Replace(plainSource, `models: ["pricing-usage-model"]`, `models: ["pricing-plain-model"]`, 1)
	_, err = jsplugin.DefaultRegistry.Register(plainSource, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister("pricing-plain") })

	const storedRelayExpr = `tier("standard", fixed(0.02))`
	const relayExpr = `tier("standard", fixed(0.01))`
	options := map[string]string{
		"billing_setting.billing_mode":          `{"pricing-relay-model":"tiered_expr","pricing-plain-model":"tiered_expr"}`,
		"billing_setting.billing_expr":          `{"pricing-relay-model":"tier(\"standard\", fixed(0.02))","pricing-plain-model":"u(\"image_count\") * 0.01"}`,
		billing_setting.PluginBillingExprOption: `{}`,
	}
	// The admin snapshot reads the options table; the validators read the
	// in-memory config.
	for key, value := range options {
		require.NoError(t, DB.Save(&Option{Key: key, Value: value}).Error)
	}
	t.Cleanup(func() {
		for key := range options {
			DB.Where("key = ?", key).Delete(&Option{})
		}
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(options))
	snapshot, err := GetModelPricingSnapshot([]string{"pricing-relay-model", "pricing-plain-model"})
	require.NoError(t, err)
	entries := make(map[string]ModelPricingEntry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entries[entry.ModelName] = entry
	}

	relay := entries["pricing-relay-model"].PluginVariants
	require.Len(t, relay, 1, "a relay-shared model lists its single plugin as a provider variant")
	assert.Equal(t, "pricing-relay", relay[0].PluginKey)
	assert.Equal(t, storedRelayExpr, relay[0].Effective)
	assert.False(t, relay[0].Compatible, "fixed() cannot price the plugin path")
	assert.Contains(t, relay[0].UsageSchema, "image_count")

	// Ordinary channels may compete on any plugin endpoint, so a plugin-only
	// model lists its provider variant too.
	plain := entries["pricing-plain-model"].PluginVariants
	require.Len(t, plain, 1)
	assert.Equal(t, "pricing-plain", plain[0].PluginKey)
	assert.True(t, plain[0].Compatible)

	// Saving a new fixed expression at model level needs the provider variant;
	// with it, both paths are priced.
	draft := PricingValues{"billing_setting.billing_expr": relayExpr}
	require.ErrorContains(t, ValidateModelPricing("pricing-relay-model", draft), "fixed pricing")
	draft[billing_setting.PluginBillingExprOption] = map[string]any{"pricing-relay": `u("image_count") * 0.01`}
	require.NoError(t, ValidateModelPricing("pricing-relay-model", draft))
}
