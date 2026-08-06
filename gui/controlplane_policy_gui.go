//go:build !service
// +build !service

package main

import (
	"sync"
	"time"

	"controlplane"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// The GUI does not evaluate org policy. It asks the service.
//
// WHY THIS FILE EXISTS: the gate is `ControlPolicy().FileRestore`, checked in
// imagebrowse_core.go and controlplane_browse.go — and those are Wails
// bindings, so on a managed machine they run IN THE GUI PROCESS. The shared
// implementation read `cpAgent`, which is nil there, and a nil agent meant
// "standalone install, nothing to enforce". The result was that org
// file-restore policy did not apply to the front end at all: exactly the
// surface it was written to cover.
//
// The service holds the agent, so the service holds the answer — including
// break-glass, which it has already folded into the value it reports. The GUI
// takes the effective policy and does not second-guess it.
//
// FAIL CLOSED. If a control server IS configured and the service cannot tell
// us the policy, restore is refused. That is the opposite of the choice made
// for backups (controlplane/unmanaged.go) and for the same underlying reason:
// the cost of being wrong runs in opposite directions. Refusing a restore
// during an outage is an inconvenience with a documented override; permitting
// one is the data leaving the building.

const guiPolicyTTL = 10 * time.Second

var (
	guiPolicyMu      sync.Mutex
	guiPolicyCached  controlplane.Policy
	guiPolicyFetched time.Time
)

func ControlPolicy() controlplane.Policy {
	guiPolicyMu.Lock()
	defer guiPolicyMu.Unlock()

	// ControlPolicy is consulted on every browse and every restore call, and
	// a browse can make hundreds in a directory walk. A short TTL keeps that
	// off the wire without letting a policy change sit stale for long.
	if time.Since(guiPolicyFetched) < guiPolicyTTL {
		return guiPolicyCached
	}

	p := fetchPolicyFromService()
	guiPolicyCached = p
	guiPolicyFetched = time.Now()
	return p
}

var (
	guiPolicyClientOnce sync.Once
	guiPolicyClient     *api.Client
)

// controlPlaneStatusClient is a local-API client of its own rather than the
// App's, because ControlPolicy is a package function called from shared code
// that has no App to hand. The client is stateless apart from the token path,
// so a second one costs nothing.
func controlPlaneStatusClient() *api.Client {
	guiPolicyClientOnce.Do(func() {
		guiPolicyClient = api.NewClient(getAPITokenPath())
	})
	return guiPolicyClient
}

// fetchPolicyFromService asks the local API for the effective policy.
func fetchPolicyFromService() controlplane.Policy {
	client := controlPlaneStatusClient()
	if client == nil {
		return controlplane.Policy{} // fail closed: nothing to ask
	}
	st, err := client.GetControlPlaneStatus()
	if err != nil || st == nil {
		writeDebugLog("[policy] service did not answer; restore stays disabled until it does")
		return controlplane.Policy{}
	}

	configured, _ := st["configured"].(bool)
	if !configured {
		// No control server at all. Ungoverned by design — the project
		// ships and is supported without NimbusControl.
		return controlplane.Policy{FileRestore: true}
	}

	known, _ := st["policy_known"].(bool)
	if !known {
		writeDebugLog("[policy] service reports no agent yet; restore stays disabled")
		return controlplane.Policy{}
	}

	fileRestore, _ := st["policy_file_restore"].(bool)
	restrict, _ := st["restrict_unmanaged_backups"].(bool)
	return controlplane.Policy{
		FileRestore:              fileRestore,
		RestrictUnmanagedBackups: restrict,
	}
}
