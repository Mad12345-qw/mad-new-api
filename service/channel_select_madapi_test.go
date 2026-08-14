package service

import "testing"

func TestRetryParamTracksFailedChannels(t *testing.T) {
	param := &RetryParam{}
	param.ExcludeChannel(63)
	param.ExcludeChannel(50)
	param.ExcludeChannel(63)
	if len(param.ExcludedChannelIDs) != 2 {
		t.Fatalf("excluded channels = %v, want two unique channels", param.ExcludedChannelIDs)
	}
	if _, ok := param.ExcludedChannelIDs[63]; !ok {
		t.Fatal("channel 63 was not retained")
	}
}
