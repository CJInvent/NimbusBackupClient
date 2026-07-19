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
	"path/filepath"

	"controlplane"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: profilecheck <profile.json>")
		os.Exit(2)
	}
	// The path is named by the operator building the installer, on their own
	// machine, and reaching an arbitrary file is the entire point of a
	// validator. gosec's taint analysis cannot know that, hence the explicit
	// waiver — G703 for the argv taint, G304 for the variable path.
	path := filepath.Clean(os.Args[1])
	raw, err := os.ReadFile(path) // #nosec G304,G703 -- operator-supplied path to a build-time CLI
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read %s: %v\n", path, err)
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
