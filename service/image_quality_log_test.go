package service

import (
	"testing"

	"github.com/QuantumNous/new-api/model"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestAppendImageQualityPricingInfo(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{
			name:   "user private quality price",
			source: "user_image_quality_private",
		},
		{
			name:   "model public quality price",
			source: "model_image_quality_public",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := uint(2)
			info := &relaycommon.RelayInfo{
				Request:                 &dto.ImageRequest{N: &n, Quality: "high", Size: "2048x2048"},
				ImageQualityPriceSource: tc.source,
				ImageQuality:            "high",
				ImageResolution:         "2k",
				ImageQualityUnitPrice:   3,
				PriceData: types.PriceData{
					UsePrice:   true,
					ModelPrice: 3,
				},
			}
			info.PriceData.AddOtherRatio("n", 2)

			other := model.NewLogOther()
			appendImageQualityPricingInfo(info, other)

			require.Equal(t, tc.source, other.Snapshot()["price_source"])
			require.Equal(t, "high", other.Snapshot()["quality"])
			require.Equal(t, "2k", other.Snapshot()["resolution"])
			require.Equal(t, float64(3), other.Snapshot()["unit_price"])
			require.Equal(t, float64(2), other.Snapshot()["image_count"])
		})
	}
}

func TestAppendImageQualityPricingInfoUsesRequestCountFallback(t *testing.T) {
	n := uint(3)
	info := &relaycommon.RelayInfo{
		Request:                 &dto.ImageRequest{N: &n},
		ImageQualityPriceSource: "model_image_quality_public",
		ImageQuality:            "medium",
		ImageResolution:         "1k",
		ImageQualityUnitPrice:   1.25,
	}

	other := model.NewLogOther()
	appendImageQualityPricingInfo(info, other)

	require.Equal(t, float64(3), other.Snapshot()["image_count"])
}

func TestAppendImageQualityPricingInfoDefaultsImageCountToOne(t *testing.T) {
	info := &relaycommon.RelayInfo{
		ImageQualityPriceSource: "model_image_quality_public",
		ImageQuality:            "low",
		ImageResolution:         "1k",
		ImageQualityUnitPrice:   0.5,
	}

	other := model.NewLogOther()
	appendImageQualityPricingInfo(info, other)

	require.Equal(t, float64(1), other.Snapshot()["image_count"])
}

func TestAppendImageQualityPricingInfoOmitsNormalRequest(t *testing.T) {
	other := model.NewLogOther()
	appendImageQualityPricingInfo(&relaycommon.RelayInfo{}, other)

	require.NotContains(t, other.Snapshot(), "price_source")
	require.NotContains(t, other.Snapshot(), "quality")
	require.NotContains(t, other.Snapshot(), "resolution")
	require.NotContains(t, other.Snapshot(), "unit_price")
	require.NotContains(t, other.Snapshot(), "image_count")
}
