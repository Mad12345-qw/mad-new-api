package common

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
)

func TestIsImageGenerationModelRecognizesGrokImagineImages(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"grok-imagine-image", true},
		{"grok-imagine-image-quality", true},
		{"grok-imagine-image-pro", true},
		{"gpt-image-2", true},
		{"gpt-image-2-4k", true},
		{"grok-imagine-video", false},
		{"grok-4.5", false},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			if got := IsImageGenerationModel(test.model); got != test.want {
				t.Fatalf("IsImageGenerationModel(%q) = %v, want %v", test.model, got, test.want)
			}
		})
	}
}

func TestGrokImagineImagePrefersImageGenerationEndpoint(t *testing.T) {
	endpoints := GetEndpointTypesByChannelType(constant.ChannelTypeXai, "grok-imagine-image-quality")
	if len(endpoints) == 0 || endpoints[0] != constant.EndpointTypeImageGeneration {
		t.Fatalf("endpoint types = %v, want image generation first", endpoints)
	}
}
