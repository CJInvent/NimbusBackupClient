package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Self-update's wire (NimbusControl docs/V4-CLIENT-RELEASES.md §7): the MSI
// is fetched for the offered version only, and nothing that does not measure
// what the offer said is left on disk.
func TestAnUpdateIsKeptOnlyWhenItMatchesTheOffer(t *testing.T) {
	msi := []byte(strings.Repeat("MSI", 1000))
	sum := sha256.Sum256(msi)
	var asked string
	d := newDPServer(t, map[string]func(http.ResponseWriter){})
	d.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RequestURI() + " " + r.Header.Get("Authorization")
		_, _ = w.Write(msi)
	})
	c := d.client()
	dir := t.TempDir()
	good := UpdateOffer{Version: "1.01.03-dev.57", Size: int64(len(msi)), SHA256: hex.EncodeToString(sum[:])}
	p := filepath.Join(dir, "a.msi")
	if err := c.DownloadUpdate(context.Background(), good, p); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(asked, "/api/agent/v1/update/msi?version=1.01.03-dev.57 Bearer 17.") {
		t.Fatalf("asked %q", asked)
	}
	if b, _ := os.ReadFile(p); string(b) != string(msi) {
		t.Fatal("the file is not the bytes served")
	}
	for name, o := range map[string]UpdateOffer{
		"wrong hash": {Version: good.Version, Size: good.Size, SHA256: strings.Repeat("0", 64)},
		"wrong size": {Version: good.Version, Size: good.Size - 1, SHA256: good.SHA256},
	} {
		q := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if err := c.DownloadUpdate(context.Background(), o, q); err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if _, err := os.Stat(q); !os.IsNotExist(err) {
			t.Fatalf("%s: a file that did not verify was left on disk", name)
		}
	}
	for _, bad := range []UpdateOffer{{Version: "4.0.0-dev.220", Size: 1, SHA256: good.SHA256}, {Version: good.Version, Size: 0, SHA256: good.SHA256},
		{Version: good.Version, Size: MaxUpdateBytes + 1, SHA256: good.SHA256}} {
		if err := c.DownloadUpdate(context.Background(), bad, filepath.Join(dir, "x")); err == nil {
			t.Fatalf("a malformed offer was fetched: %+v", bad)
		}
	}
}

func TestTheCheckinCarriesTheUpdateAndTheLine(t *testing.T) {
	var r CheckinResponse
	if err := json.Unmarshal([]byte(`{"commands":[],"update":{"version":"1.01.03","size":5,"sha256":"`+strings.Repeat("a", 64)+
		`"},"agent_line":{"state":"older","needs":"1.01"}}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Update == nil || r.Update.Version != "1.01.03" || r.Update.Size != 5 || r.AgentLine.Compatible() || r.AgentLine.Needs != "1.01" {
		t.Fatalf("%+v %+v", r.Update, r.AgentLine)
	}
	var old CheckinResponse
	_ = json.Unmarshal([]byte(`{"commands":[]}`), &old)
	if old.Update != nil || !old.AgentLine.Compatible() {
		t.Fatal("a server that says nothing about lines is compatible and offers nothing")
	}
}
