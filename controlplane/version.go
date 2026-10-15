package controlplane

import (
	"fmt"
	"regexp"
	"strconv"
)

// Release versions, L.CC.RR[-channel.N] (NimbusControl
// docs/V4-CLIENT-RELEASES.md section 1). L is the product line, CC the
// compatibility number, RR the release inside it; a server and an agent work
// together only when L.CC is equal. The server has the same rules in
// Releases\Version; both are held to testdata/version-fixtures.json.

// Channels in order: a release of a later channel is offered to everything
// following an earlier one.
var Channels = []string{"dev", "alpha", "beta", "stable"}

var versionRe = regexp.MustCompile(`^v?(\d+)\.(\d{2,})\.(\d{2,})(?:-(dev|alpha|beta)\.(\d+))?$`)

// Version is one parsed release version.
type Version struct {
	L, CC, RR int
	Channel   string
	Build     int
}

// ParseVersion parses a version, with or without the leading v.
func ParseVersion(s string) (Version, bool) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	atoi := func(x string) (int, bool) {
		n, err := strconv.Atoi(x)
		return n, err == nil
	}
	var v Version
	var ok bool
	if v.L, ok = atoi(m[1]); !ok {
		return Version{}, false
	}
	if v.CC, ok = atoi(m[2]); !ok {
		return Version{}, false
	}
	if v.RR, ok = atoi(m[3]); !ok {
		return Version{}, false
	}
	v.Channel = "stable"
	if m[4] != "" {
		v.Channel = m[4]
		if v.Build, ok = atoi(m[5]); !ok {
			return Version{}, false
		}
	}
	return v, true
}

// Line is L.CC as RELEASE_LINE writes it.
func (v Version) Line() string { return fmt.Sprintf("%d.%02d", v.L, v.CC) }

func (v Version) String() string {
	base := fmt.Sprintf("%d.%02d.%02d", v.L, v.CC, v.RR)
	if v.Channel == "stable" {
		return base
	}
	return fmt.Sprintf("%s-%s.%d", base, v.Channel, v.Build)
}

func channelRank(c string) int {
	for i, x := range Channels {
		if x == c {
			return i
		}
	}
	return -1
}

// CompareVersions orders L, CC, RR numerically, then dev < alpha < beta <
// stable, then the build number.
func CompareVersions(a, b Version) int {
	for _, d := range [][2]int{{a.L, b.L}, {a.CC, b.CC}, {a.RR, b.RR},
		{channelRank(a.Channel), channelRank(b.Channel)}, {a.Build, b.Build}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// CompatibleWith is true when both are on the same L.CC.
func (v Version) CompatibleWith(o Version) bool { return v.L == o.L && v.CC == o.CC }

// Follows reports whether a release on channel is offered to something
// following follow (stable takes stable; beta also stable; alpha also beta
// and stable; dev takes everything).
func Follows(follow, channel string) bool {
	f, c := channelRank(follow), channelRank(channel)
	return f >= 0 && c >= 0 && c >= f
}
