package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"os"
	"sort"
	"testing"
)

// The same file the server tests against (NimbusControl
// docs/version-fixtures.json), vendored byte-identical. Both pin its hash.
const versionFixtureSHA256 = "3b38c6906442ec27b670fc6dfb4c2d246d3c607bb4d60f5b241c52eee7817b92"

type versionFixtures struct {
	Parse []struct {
		In               string
		OK               bool
		L, CC, RR, Build int
		Channel, Line    string
	} `json:"parse"`
	Order      []string `json:"order"`
	Compatible []struct {
		A, B string
		OK   bool
	} `json:"compatible"`
	Follows []struct {
		Channel string
		Accepts []string
	} `json:"follows"`
}

func TestVersionsAgreeWithTheServersFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/version-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != versionFixtureSHA256 {
		t.Fatal("testdata/version-fixtures.json is not the file the server tests against")
	}
	var fx versionFixtures
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for _, c := range fx.Parse {
		v, ok := ParseVersion(c.In)
		if ok != c.OK {
			t.Errorf("parse %q: ok=%v want %v", c.In, ok, c.OK)
			continue
		}
		if ok && (v.L != c.L || v.CC != c.CC || v.RR != c.RR || v.Channel != c.Channel || v.Build != c.Build || v.Line() != c.Line) {
			t.Errorf("parse %q: %+v line %s", c.In, v, v.Line())
		}
	}
	vs := make([]Version, len(fx.Order))
	for i, s := range fx.Order {
		v, ok := ParseVersion(s)
		if !ok {
			t.Fatalf("order entry %q does not parse", s)
		}
		vs[i] = v
	}
	shuffled := append([]Version(nil), vs...)
	rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	sort.Slice(shuffled, func(i, j int) bool { return CompareVersions(shuffled[i], shuffled[j]) < 0 })
	for i := range vs {
		if shuffled[i].String() != fx.Order[i] {
			t.Fatalf("sorted order differs at %d: %s want %s", i, shuffled[i], fx.Order[i])
		}
	}
	for _, c := range fx.Compatible {
		a, okA := ParseVersion(c.A)
		b, okB := ParseVersion(c.B)
		if got := okA && okB && a.CompatibleWith(b); got != c.OK {
			t.Errorf("compatible %s %s: %v want %v", c.A, c.B, got, c.OK)
		}
	}
	for _, c := range fx.Follows {
		for _, ch := range Channels {
			want := false
			for _, a := range c.Accepts {
				want = want || a == ch
			}
			if Follows(c.Channel, ch) != want {
				t.Errorf("following %s takes %s: %v", c.Channel, ch, !want)
			}
		}
	}
}
