package controlplane

import (
	"net"
	"strings"
)

// Reporting this machine's LAN interfaces (NimbusControl
// V4-SECURITY-ROADMAP §5, wire contract in its docs/AGENT-API.md).
//
// WHAT IS AND IS NOT OURS TO SAY. Only this machine can see its own
// interfaces, so the LAN half of address tracking has to come from here. The
// PUBLIC address does not: the server observes it from the socket the
// check-in arrives on, and an address the agent asserts is one the agent
// could be wrong -- or lying -- about. So this file never tries to discover a
// public address, and there is deliberately no echo-service call anywhere in
// it. That inverts the DynDNS/CGNAT updater shape rather than reimplementing
// it, and it is what makes the recorded address worth anything when someone
// later asks whether a re-adopted machine is where it claims to be.

// MaxReportedAddresses caps the (interface, address) pairs one check-in
// carries.
//
// The server keeps at most this many from a single check-in and discards the
// rest. Capping here too means the machine chooses WHICH of its addresses
// survive -- in its own interface order, which is the order that means
// something -- instead of whichever ones happened to be read first on the far
// side. It also keeps a machine with a pathological interface list (a host
// running dozens of container bridges) from spending its inventory budget on
// addresses nobody will look at.
const MaxReportedAddresses = 16

// ifaceView is one interface as the OS describes it, narrowed to what the
// selection actually reads.
//
// It exists so the selection is testable without a machine: a test that asked
// net.Interfaces() what this host has could only assert that the code agrees
// with the code (dev rule 28), and would say something different on every
// build agent it ran on.
type ifaceView struct {
	Name     string
	Up       bool
	Loopback bool
	Addrs    []string // as the OS gives them: "10.0.0.4/24", or a bare IP
}

// LocalInterfaces reports the interfaces worth telling the server about.
//
// Returns nil, not an error, when the interface list cannot be read: this is
// display telemetry on the check-in path, and a machine whose interfaces
// cannot be enumerated must still check in, still receive its commands and
// still back up. An absent list means "no reading", which is exactly what the
// server stores for it.
func LocalInterfaces() []NetworkInterface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}

	views := make([]ifaceView, 0, len(ifs))
	for _, i := range ifs {
		addrs, err := i.Addrs()
		if err != nil {
			// One unreadable interface is not a reason to report none.
			continue
		}
		as := make([]string, 0, len(addrs))
		for _, a := range addrs {
			as = append(as, a.String())
		}
		views = append(views, ifaceView{
			Name:     i.Name,
			Up:       i.Flags&net.FlagUp != 0,
			Loopback: i.Flags&net.FlagLoopback != 0,
			Addrs:    as,
		})
	}
	return selectInterfaces(views)
}

// selectInterfaces is the whole decision: which interfaces, which addresses,
// and how many.
//
// Loopback and link-local are dropped here even though the server drops them
// too. That is not a duplicated rule: the server's copy is the backstop
// against an untrusted payload, and this copy is what keeps 169.254/fe80
// noise -- which every machine has and which identifies none of them -- from
// consuming the pair budget before a real address is reached.
func selectInterfaces(ifs []ifaceView) []NetworkInterface {
	var out []NetworkInterface
	seen := make(map[string]bool)
	pairs := 0

	for _, i := range ifs {
		if !i.Up || i.Loopback {
			continue
		}

		var kept []string
		for _, raw := range i.Addrs {
			if pairs >= MaxReportedAddresses {
				break
			}

			// Addrs() yields CIDR form ("10.0.0.4/24"); the mask is not
			// part of the address and the server stores an address.
			s := raw
			if idx := strings.IndexByte(s, '/'); idx >= 0 {
				s = s[:idx]
			}
			// An IPv6 zone ("fe80::1%eth0") would be dropped below as
			// link-local anyway, but strip it rather than depend on that:
			// the zone IS the interface, which already has its own field.
			if idx := strings.IndexByte(s, '%'); idx >= 0 {
				s = s[:idx]
			}

			ip := net.ParseIP(s)
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() ||
				ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
				continue
			}

			// Normalized form, so the same address written two ways is one
			// address here rather than two rows there.
			s = ip.String()
			key := i.Name + "\x00" + s
			if seen[key] {
				continue
			}
			seen[key] = true
			kept = append(kept, s)
			pairs++
		}

		// An interface with nothing left to say is not reported: an entry
		// with an empty address list is a row the server would have to
		// decide what to do with, and there is nothing to decide.
		if len(kept) > 0 {
			out = append(out, NetworkInterface{Name: i.Name, Addresses: kept})
		}
		if pairs >= MaxReportedAddresses {
			break
		}
	}

	return out
}
