package model

import "testing"

func TestNormalizeImageQualityMapsMaxAliasesToHigh(t *testing.T) {
	for _, raw := range []string{"max", "MAX", " Max ", "maximum", "best"} {
		if got := NormalizeImageQuality(raw); got != ImageQualityHigh {
			t.Errorf("NormalizeImageQuality(%q) = %q, want %q", raw, got, ImageQualityHigh)
		}
	}
}
