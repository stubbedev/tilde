package wm

import (
	"image"
	"regexp"
	"testing"
)

func TestSteamRules(t *testing.T) {
	steam := regexp.MustCompile(`^steam$`)
	rules := []Rule{
		{AppID: steam, Mode: RuleFloat},
		{AppID: steam, Title: regexp.MustCompile(`^Steam$`), Mode: RuleTile},
		{AppID: steam, Title: regexp.MustCompile(`^Friends List$`), Size: image.Pt(400, 900)},
	}
	cases := []struct {
		app, title string
		want       Decision
	}{
		{"steam", "Steam", Decision{Mode: RuleTile}},
		{"steam", "Friends List", Decision{Mode: RuleFloat, Size: image.Pt(400, 900)}},
		{"steam", "Steam Settings", Decision{Mode: RuleFloat}},
		{"foot", "Steam", Decision{}},
	}
	for _, c := range cases {
		if got := Decide(rules, c.app, c.title); got != c.want {
			t.Errorf("%s %q: %+v, want %+v", c.app, c.title, got, c.want)
		}
	}
}
