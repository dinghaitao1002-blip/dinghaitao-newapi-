package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCustomPricingModesRemainSaveableAfterUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pricing PricingValues
		valid   bool
	}{
		{"image_quality", PricingValues{"billing_setting.billing_mode": "image_quality"}, true},
		{"video_matrix", PricingValues{"billing_setting.billing_mode": "video_per_second", "billing_setting.billing_expr": `{"480p":0,"720p":0.05,"1080p":0.1,"4k":0.2}`}, true},
		{"video_missing", PricingValues{"billing_setting.billing_mode": "video_per_second"}, false},
		{"video_negative", PricingValues{"billing_setting.billing_mode": "video_per_second", "billing_setting.billing_expr": `{"720p":-0.05}`}, false},
		{"video_not_matrix", PricingValues{"billing_setting.billing_mode": "video_per_second", "billing_setting.billing_expr": `tier("base", fixed(0.05))`}, false},
		{"expression_not_matrix", PricingValues{"billing_setting.billing_mode": "tiered_expr", "billing_setting.billing_expr": `{"720p":0.05}`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModelPricing("upgrade-custom-pricing", tc.pricing)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
