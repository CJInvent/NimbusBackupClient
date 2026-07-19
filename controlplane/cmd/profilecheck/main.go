// Command profilecheck validates a NimbusControl provisioning profile against
// the contract in docs/MSI-PROVISIONING.md.
//
// It exists so an MSP's build pipeline rejects a broken profile on the
// workstation building the MSI, rather than at first boot on a customer
// endpoint where the only symptom is a machine that never appears in the
// fleet. It deliberately calls the SAME parser the agent uses, so build-time
// and run-time can never disagree about what a valid profile is.
//
//	go run ./cmd/profilecheck acme.json
//
// Exit codes: 0 valid, 1 invalid or unreadable, 2 usage.
package main

import (
	"fmt"
	"os"

	"controlplane"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: profilecheck <profile.json>")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1]) // #nosec G304 -- path supplied by the operator building the MSI
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
	p, err := controlplane.ParseProfile(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "INVALID: %v\n", err)
		os.Exit(1)
	}
	// Redacted: this runs in build logs and CI output, and the profile holds a
	// live org enrollment token.
	fmt.Printf("valid (contract v%d): %s\n", controlplane.ProfileVersion, p.Redacted())
}
