package main

import (
	"strings"
	"testing"
)

func TestPrimaryGPUFollowsTheRule(t *testing.T) {
	igpu := gpuCard{name: "card0", render: true}
	dgpu := gpuCard{name: "card1", render: true, bootVGA: true}
	vkms := gpuCard{name: "card2"}
	cases := []struct {
		name       string
		cards      []gpuCard
		configured string
		want       int
		why        string
	}{
		{"the config wins", []gpuCard{igpu, dgpu}, "card0", 0, "set by gpu in the config"},
		{"then the boot VGA device", []gpuCard{igpu, dgpu}, "", 1, "the boot VGA device"},
		{"a boot VGA device without a render node is skipped", []gpuCard{{name: "card0", bootVGA: true}, igpu}, "", 1, "the first card with a render node"},
		{"then the first render node", []gpuCard{vkms, igpu}, "", 1, "the first card with a render node"},
		{"display-only cards render in software as a last resort", []gpuCard{vkms}, "", 0, "the first card; none has a render node"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why, err := choosePrimary(tc.cards, tc.configured)
			if err != nil || got != tc.want || !strings.HasPrefix(why, tc.why) {
				t.Fatalf("choosePrimary = %d, %q, %v", got, why, err)
			}
		})
	}
	if _, _, err := choosePrimary([]gpuCard{igpu}, "card7"); err == nil || !strings.Contains(err.Error(), "card7 is not a DRM card on this seat (card0)") {
		t.Fatalf("a configured card that is missing: %v", err)
	}
	if _, _, err := choosePrimary(nil, ""); err == nil {
		t.Fatal("no cards chose a primary")
	}
}
