package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadFramePreservesRawPayload(t *testing.T) {
	metaBytes, err := json.Marshal(dispatchMeta{ChannelID: 66, Model: "gpt-image-2"})
	require.NoError(t, err)
	payload := []byte{0, 1, 2, 3, 254, 255}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(metaBytes)))
	input := bytes.Join([][]byte{prefix[:], metaBytes, payload}, nil)
	meta, got, err := readFrame(bytes.NewReader(input))
	require.NoError(t, err)
	require.Equal(t, 66, meta.ChannelID)
	require.Equal(t, payload, got)
}

func TestProviderMappingAndBootstrapSafety(t *testing.T) {
	require.Equal(t, "claude", providerForChannel(14))
	require.Equal(t, "gemini", providerForChannel(24))
	require.Equal(t, "xai", providerForChannel(48))
	require.Equal(t, "codex", providerForChannel(57))
	require.Equal(t, compatProvider, providerForChannel(1))
	config := bootstrapConfig(18317, "/tmp/auth")
	require.Contains(t, config, "request-retry: 0")
	require.Contains(t, config, "disable-image-generation: \"passthrough\"")
}

func TestRequestModelInfoRegistersExactSelectedModel(t *testing.T) {
	textModel := requestModelInfo(dispatchMeta{Model: "gpt-5.6-terra", Source: "openai-response"}, compatProvider)
	require.Equal(t, "gpt-5.6-terra", textModel.ID)
	require.Equal(t, compatProvider, textModel.Type)
	require.Equal(t, []string{"TEXT"}, textModel.SupportedOutputModalities)
	require.True(t, textModel.UserDefined)
	require.True(t, textModel.IsCompat)

	imageModel := requestModelInfo(dispatchMeta{Model: "gpt-image-2", Source: "openai-image"}, compatProvider)
	require.Equal(t, "gpt-image-2", imageModel.ID)
	require.Equal(t, "openai-image", imageModel.Type)
	require.Equal(t, []string{"IMAGE"}, imageModel.SupportedOutputModalities)
}
