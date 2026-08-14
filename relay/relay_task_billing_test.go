package relay

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/stretchr/testify/require"
)

func TestApplyTaskBillingRatiosKeepsUnrelatedFixedRequestPrice(t *testing.T) {
	info := &relaycommon.RelayInfo{
		PriceData: types.PriceData{UsePrice: true, Quota: 480000},
	}
	info.PriceData.AddOtherRatio("seconds", 5)
	info.PriceData.AddOtherRatio("size", 1.666667)

	applyTaskBillingRatios(info, "unrelated-fixed-video")

	require.Equal(t, 480000, info.PriceData.Quota)
}

func TestApplyTaskBillingRatiosBillsGrok15BySecondsAndResolution(t *testing.T) {
	tests := []struct {
		model      string
		seconds    float64
		resolution float64
		images     float64
		want       int
	}{
		{model: "grok-imagine-video-1.5", seconds: 1, resolution: 1, want: 224000},
		{model: "grok-imagine-video-1.5", seconds: 15, resolution: 1, want: 3360000},
		{model: "grok-imagine-video-1.5", seconds: 1, resolution: 25.0 / 14.0, want: 400000},
		{model: "grok-imagine-video-1.5", seconds: 15, resolution: 25.0 / 14.0, want: 6000000},
		{model: "grok-imagine-video-1.5-preview", seconds: 1, resolution: 1, want: 224000},
		{model: "grok-imagine-video-1.5-preview", seconds: 15, resolution: 25.0 / 14.0, images: 1, want: 6000000},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				PriceData: types.PriceData{UsePrice: true, Quota: 224000},
			}
			info.PriceData.AddOtherRatio("seconds", tt.seconds)
			info.PriceData.AddOtherRatio("resolution", tt.resolution)
			info.PriceData.AddOtherRatio("images", tt.images)

			applyTaskBillingRatios(info, tt.model)

			require.Equal(t, tt.want, info.PriceData.Quota)
		})
	}
}

func TestApplyTaskBillingRatiosBillsOriginalGrokImageAdditively(t *testing.T) {
	info := &relaycommon.RelayInfo{
		PriceData: types.PriceData{UsePrice: true, Quota: 112000},
	}
	info.PriceData.AddOtherRatio("seconds", 4)
	info.PriceData.AddOtherRatio("resolution", 1)
	info.PriceData.AddOtherRatio("images", 1)

	applyTaskBillingRatios(info, "grok-imagine-video")

	require.Equal(t, 451200, info.PriceData.Quota)
}

func TestApplyTaskBillingRatiosBillsOriginalGrokWithoutImage(t *testing.T) {
	info := &relaycommon.RelayInfo{
		PriceData: types.PriceData{UsePrice: true, Quota: 112000},
	}
	info.PriceData.AddOtherRatio("seconds", 15)
	info.PriceData.AddOtherRatio("resolution", 1)
	info.PriceData.AddOtherRatio("images", 0)

	applyTaskBillingRatios(info, "grok-imagine-video")

	require.Equal(t, 1680000, info.PriceData.Quota)
}

func TestApplyTaskBillingRatiosBillsEachOriginalGrokInputImage(t *testing.T) {
	info := &relaycommon.RelayInfo{
		PriceData: types.PriceData{UsePrice: true, Quota: 112000},
	}
	info.PriceData.AddOtherRatio("seconds", 1)
	info.PriceData.AddOtherRatio("resolution", 1)
	info.PriceData.AddOtherRatio("images", 3)

	applyTaskBillingRatios(info, "grok-imagine-video")

	require.Equal(t, 121600, info.PriceData.Quota)
}

func TestApplyTaskBillingRatiosBillsSeedance37ByRequestedSeconds(t *testing.T) {
	tests := []struct {
		model   string
		quota   int
		seconds float64
		want    int
	}{
		{model: "seedance-2.0-1080p", quota: 300000, seconds: 4, want: 1200000},
		{model: "seedance-2.0-720p", quota: 220000, seconds: 4, want: 880000},
		{model: "seedance-fast-720p", quota: 200000, seconds: 4, want: 800000},
		{model: "seedance-fast-720p", quota: 200000, seconds: 15, want: 3000000},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				PriceData: types.PriceData{UsePrice: true, Quota: tt.quota},
			}
			info.PriceData.AddOtherRatio("seconds", tt.seconds)
			info.PriceData.AddOtherRatio("size", 1.666667)

			applyTaskBillingRatios(info, tt.model)

			require.Equal(t, tt.want, info.PriceData.Quota)
		})
	}
}

func TestApplyTaskBillingRatiosKeepsLegacySeedanceFixedPrice(t *testing.T) {
	for _, modelName := range []string{
		"seedance-2.0",
		"seedance-2.0-fast",
		"doubao-seedance-2.0-720p",
		"doubao-seedance-2.0-cf-1080p",
	} {
		info := &relaycommon.RelayInfo{
			PriceData: types.PriceData{UsePrice: true, Quota: 600000},
		}
		info.PriceData.AddOtherRatio("seconds", 4)

		applyTaskBillingRatios(info, modelName)

		require.Equal(t, 600000, info.PriceData.Quota)
	}
}

func TestApplyAdjustedTaskBillingRatiosKeepsFixedRequestPrice(t *testing.T) {
	info := &relaycommon.RelayInfo{
		PriceData: types.PriceData{UsePrice: true, Quota: 480000},
	}
	info.PriceData.AddOtherRatio("seconds", 5)

	quota, ok := applyAdjustedTaskBillingRatios(info, map[string]float64{
		"seconds": 10,
		"size":    1.666667,
	})

	require.True(t, ok)
	require.Equal(t, 480000, quota)
	require.Equal(t, 480000, info.PriceData.Quota)
	require.Equal(t, 10.0, info.PriceData.OtherRatios()["seconds"])
}

func TestApplyTaskBillingRatiosStillAppliesUsageBasedMultipliers(t *testing.T) {
	info := &relaycommon.RelayInfo{
		PriceData: types.PriceData{UsePrice: false, Quota: 100},
	}
	info.PriceData.AddOtherRatio("seconds", 5)

	applyTaskBillingRatios(info, "usage-priced-video")

	require.Equal(t, 500, info.PriceData.Quota)
}
