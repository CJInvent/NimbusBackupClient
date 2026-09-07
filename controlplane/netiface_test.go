package controlplane

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The selection is tested against a fabricated interface list, never against
// this host's real one: net.Interfaces() answers something different on every
// developer machine and every build agent, so a test that asked it would
// assert only that the code agrees with itself (dev rule 28) and would pass or
// fail for reasons that have nothing to do with the change under test.

func names(ifs []NetworkInterface) []string {
	out := make([]string, 0, len(ifs))
	for _, i := range ifs {
		out = append(out, i.Name+"="+fmt.Sprint(i.Addresses))
	}
	return out
}

func TestSelectInterfacesKeepsWhatIdentifiesTheMachine(t *testing.T) {
	got := selectInterfaces([]ifaceView{
		{Name: "lo", Up: true, Loopback: true, Addrs: []string{"127.0.0.1/8", "::1/128"}},
		{Name: "Ethernet", Up: true, Addrs: []string{
			"10.10.83.42/24", // the address that matters
			"fe80::1a2b/64",  // link-local: on every machine, identifies none
			"2001:db8::5/64", // a global v6 address is worth as much as the v4
		}},
		{Name: "Wi-Fi", Up: true, Addrs: []string{"192.168.4.7/24"}},
		{Name: "Ethernet 2", Up: false, Addrs: []string{"10.9.9.9/24"}}, // down: not where this machine is
	})

	want := []string{
		"Ethernet=[10.10.83.42 2001:db8::5]",
		"Wi-Fi=[192.168.4.7]",
	}
	if fmt.Sprint(names(got)) != fmt.Sprint(want) {
		t.Fatalf("selection\n got: %v\nwant: %v", names(got), want)
	}
}

// RFC1918 addresses are the point of the LAN half, not noise to be filtered:
// most machines this agent runs on have nothing else.
func TestSelectInterfacesKeepsPrivateAddresses(t *testing.T) {
	for _, addr := range []string{"10.0.0.4/8", "172.16.3.9/12", "192.168.1.5/24", "fc00::9/7"} {
		got := selectInterfaces([]ifaceView{{Name: "e0", Up: true, Addrs: []string{addr}}})
		if len(got) != 1 || len(got[0].Addresses) != 1 {
			t.Fatalf("%s was dropped; private ranges are what this reports", addr)
		}
	}
}

func TestSelectInterfacesDropsWhatIdentifiesNothing(t *testing.T) {
	got := selectInterfaces([]ifaceView{{Name: "e0", Up: true, Addrs: []string{
		"127.0.0.1/8", // loopback on a non-loopback interface
		"::1/128",
		"169.254.7.7/16", // IPv4 link-local (APIPA)
		"fe80::1%e0",     // v6 link-local, zone id and all
		"0.0.0.0/0",      // unspecified
		"224.0.0.251/32", // multicast
		"not-an-address", // whatever the OS handed us, it is not one
	}}})
	if len(got) != 0 {
		t.Fatalf("expected nothing reportable, got %v", names(got))
	}
}

// An interface with nothing left after filtering is omitted entirely rather
// than reported with an empty list: the server would have to decide what an
// address-less interface means, and there is nothing to decide.
func TestSelectInterfacesOmitsEmptyInterfaces(t *testing.T) {
	got := selectInterfaces([]ifaceView{
		{Name: "empty", Up: true, Addrs: []string{"fe80::1/64"}},
		{Name: "real", Up: true, Addrs: []string{"10.0.0.2/24"}},
	})
	if len(got) != 1 || got[0].Name != "real" {
		t.Fatalf("expected only the interface with an address, got %v", names(got))
	}
}

func TestSelectInterfacesNormalizesAndDeduplicates(t *testing.T) {
	got := selectInterfaces([]ifaceView{{Name: "e0", Up: true, Addrs: []string{
		"10.0.0.4/24",
		"10.0.0.4",          // the same address without a mask
		"2001:0db8:0000::5", // the same v6 address, written long
		"2001:db8::5",
	}}})
	if len(got) != 1 || len(got[0].Addresses) != 2 {
		t.Fatalf("expected two distinct addresses after normalization, got %v", names(got))
	}
}

// The cap is a bound on what this code may ever send, so it is tested by
// reaching for more than it -- not by reading the constant back.
func TestSelectInterfacesIsBounded(t *testing.T) {
	var ifs []ifaceView
	for i := 1; i <= 40; i++ {
		ifs = append(ifs, ifaceView{
			Name:  fmt.Sprintf("br%d", i),
			Up:    true,
			Addrs: []string{fmt.Sprintf("172.16.0.%d/24", i)},
		})
	}
	got := selectInterfaces(ifs)

	total := 0
	for _, i := range got {
		total += len(i.Addresses)
	}
	if total != MaxReportedAddresses {
		t.Fatalf("sent %d pairs, the cap is %d", total, MaxReportedAddresses)
	}
	// And it keeps them in the machine's own order, so the cap truncates the
	// tail rather than picking arbitrarily.
	if got[0].Name != "br1" {
		t.Fatalf("first reported interface is %q; the cap must truncate, not reorder", got[0].Name)
	}
}

// An agent that reports nothing must send no key at all: the server treats an
// absent `interfaces` as "no reading from this agent", which is not the same
// as "this machine has no addresses" and is not stored as such.
func TestInventoryOmitsInterfacesWhenThereAreNone(t *testing.T) {
	b, err := json.Marshal(&Inventory{Jobs: []InventoryJob{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); strings.Contains(got, "interfaces") {
		t.Fatalf("empty interface list must be omitted from the wire, got %s", got)
	}

	b, err = json.Marshal(&Inventory{
		Jobs:       []InventoryJob{},
		Interfaces: []NetworkInterface{{Name: "e0", Addresses: []string{"10.0.0.2"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `"interfaces":[{"name":"e0","addresses":["10.0.0.2"]}]`
	if !strings.Contains(string(b), want) {
		t.Fatalf("wire shape changed; the server parses %s\n got: %s", want, string(b))
	}
}
