package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/require"
)

func TestBuildAudioQuotaInfoPreservesFixedRequestPrice(t *testing.T) {
	priceInfo := buildAudioQuotaInfo(
		TokenDetails{TextTokens: 20},
		TokenDetails{AudioTokens: 83},
		"moss-tts",
		true,
		15,
		0,
		0.06,
	)

	require.Equal(t, 15.0, priceInfo.ModelPrice)
	quota, clamp := calculateAudioQuota(priceInfo)
	require.Nil(t, clamp)
	require.Equal(t, int(15*common.QuotaPerUnit*0.06), quota)
}
