package wm

import (
	"image"
	"regexp"
)

type RuleMode uint8

const (
	RuleUnset RuleMode = iota
	RuleFloat
	RuleTile
)

type Rule struct {
	AppID *regexp.Regexp
	Title *regexp.Regexp
	Mode  RuleMode
	Size  image.Point
}

type Decision struct {
	Mode RuleMode
	Size image.Point
}

func (r Rule) matches(appID, title string) bool {
	return (r.AppID == nil || r.AppID.MatchString(appID)) && (r.Title == nil || r.Title.MatchString(title))
}

func Decide(rules []Rule, appID, title string) Decision {
	var d Decision
	for _, r := range rules {
		if !r.matches(appID, title) {
			continue
		}
		if r.Mode != RuleUnset {
			d.Mode = r.Mode
		}
		if r.Size != (image.Point{}) {
			d.Size = r.Size
		}
	}
	return d
}
