package xai

import "strings"

var ModelList = []string{
	"grok-imagine-video",
	"grok-imagine-video-1.5",
	"grok-imagine-video-1.5-preview",
}

const ChannelName = "xAI Video"

func IsVideoModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "grok-imagine-video", "grok-imagine-video-1.5", "grok-imagine-video-1.5-preview":
		return true
	default:
		return false
	}
}

func IsVideo15Model(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "grok-imagine-video-1.5", "grok-imagine-video-1.5-preview":
		return true
	default:
		return false
	}
}
