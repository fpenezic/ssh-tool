package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Update channel. "stable" (the default) is offered releases only; "rc"
// also release candidates - GitHub prereleases tagged vX.Y.Z-rcN, which CI
// publishes from the same pipeline. -test and other suffixed prereleases
// are never offered on either channel.
const (
	updateChannelKey    = "update_channel"
	updateChannelStable = "stable"
	updateChannelRC     = "rc"
)

func (a *App) updateChannel() string {
	if v, ok, _ := a.db.GetSetting(updateChannelKey); ok && v == updateChannelRC {
		return updateChannelRC
	}
	return updateChannelStable
}

// rcRe matches a release-candidate version, optionally followed by a git
// describe suffix (a build from a few commits after the RC tag).
var rcRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+-rc(\d+)(-\d+-g[0-9a-f]+)?(-dirty)?$`)

// rcTagRe: exactly a published RC tag.
var rcTagRe = regexp.MustCompile(`^v\d+\.\d+\.\d+-rc\d+$`)

// rcNumber is N for vX.Y.Z-rcN (with or without a describe suffix), 0 for
// anything else.
func rcNumber(v string) int {
	m := rcRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// isRCTag: exactly vX.Y.Z-rcN, the form CI publishes as a release
// candidate.
func isRCTag(tag string) bool {
	return rcTagRe.MatchString(strings.TrimSpace(tag))
}
