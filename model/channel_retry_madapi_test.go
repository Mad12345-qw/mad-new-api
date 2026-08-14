package model

import "testing"

func TestPreferUntriedChannelsUsesWeightsWithoutRepeating(t *testing.T) {
	channels := []*Channel{{Id: 18}, {Id: 50}, {Id: 63}}
	got := preferUntriedChannels(channels, map[int]struct{}{50: {}})
	if len(got) != 2 || got[0].Id != 18 || got[1].Id != 63 {
		t.Fatalf("untried channels = %#v, want 18 and 63", got)
	}
	got = preferUntriedChannels(channels, map[int]struct{}{18: {}, 50: {}, 63: {}})
	if len(got) != len(channels) {
		t.Fatalf("all channels exhausted = %d candidates, want %d", len(got), len(channels))
	}
}

func TestPreferUntriedAbilitiesFallsBackAfterExhaustion(t *testing.T) {
	abilities := []Ability{{ChannelId: 18}, {ChannelId: 50}}
	got := preferUntriedAbilities(abilities, map[int]struct{}{18: {}})
	if len(got) != 1 || got[0].ChannelId != 50 {
		t.Fatalf("untried abilities = %#v, want channel 50", got)
	}
	got = preferUntriedAbilities(abilities, map[int]struct{}{18: {}, 50: {}})
	if len(got) != len(abilities) {
		t.Fatalf("all abilities exhausted = %d candidates, want %d", len(got), len(abilities))
	}
}
