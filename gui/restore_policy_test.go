//go:build !service

package main

// EVERY restore-shaped entry point must honour `file_restore` (phase G).
//
// Three of them did not. `ListSnapshotsInline`, `ReadSnapshotMetaInline` and
// `SearchFilesInline` ran with no policy check at all, so an org that had
// switched file restore off on a machine still had a console that could
// enumerate snapshots, walk an archive for its metadata sidecar, and — worst
// of the three — search names, paths, sizes and mtimes across every snapshot
// in range. Search does not even go through the gated lister; it calls
// assembleSnapshotTree directly.
//
// The wire contract is explicit about this (controlplane/types.go):
//
//	FileRestore=false: the GUI must hide/disable its restore browser and the
//	local API must refuse restore operations on this machine.
//
// So this is a contract violation, not a judgement call, and the test is
// written as a SWEEP rather than a case list: every exported restore entry
// point is named here, and a new one that forgets the gate has to be added to
// this table to be considered done.
//
// THIS FILE IS `!service` BECAUSE THE GATE IT TESTS IS. The GUI's
// ControlPolicy() fetches the effective policy from the service over the local
// API and fails closed; the service build evaluates it from the agent
// directly. Pinning the GUI half is the point — that is the process the
// bindings run in, and the one where the gate was missing.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"controlplane"
)

// forcePolicy plants an effective policy in the GUI's cache and restores the
// previous state afterwards.
//
// It writes the CACHE rather than stubbing a function, deliberately: the cache
// is what ControlPolicy() actually consults, so a test that fills it exercises
// the real code path including the TTL branch. A stub would have proved that a
// replacement returned what it was told to.
func forcePolicy(t *testing.T, p controlplane.Policy) {
	t.Helper()
	guiPolicyMu.Lock()
	prevPolicy, prevFetched := guiPolicyCached, guiPolicyFetched
	guiPolicyCached, guiPolicyFetched = p, time.Now()
	guiPolicyMu.Unlock()

	t.Cleanup(func() {
		guiPolicyMu.Lock()
		guiPolicyCached, guiPolicyFetched = prevPolicy, prevFetched
		guiPolicyMu.Unlock()
	})
}

// restoreEntryPoint is one exported way into the restore engine. `call` must
// invoke it with parameters that are otherwise VALID — a function that refuses
// for a missing datastore proves nothing about the policy gate.
type restoreEntryPoint struct {
	name string
	call func() error
}

func restoreEntryPoints() []restoreEntryPoint {
	opts := RestoreOptions{
		BaseURL:      "https://pbs.invalid:8007",
		AuthID:       "test@pbs!token",
		Secret:       "not-a-real-secret",
		Datastore:    "store",
		BackupID:     "host-1",
		SnapshotTime: time.Unix(1700000000, 0),
		DestPath:     "/tmp/nimbus-policy-test",
	}
	return []restoreEntryPoint{
		{"ListSnapshotsInline", func() error {
			_, err := ListSnapshotsInline(opts.BaseURL, opts.AuthID, opts.Secret,
				opts.Datastore, opts.Namespace, opts.CertFingerprint, opts.BackupID)
			return err
		}},
		{"ListSnapshotContentsInline", func() error {
			_, err := ListSnapshotContentsInline(opts, "", false)
			return err
		}},
		{"ReadSnapshotMetaInline", func() error {
			_, err := ReadSnapshotMetaInline(opts, false)
			return err
		}},
		{"RestoreSnapshotInline", func() error {
			return RestoreSnapshotInline(opts)
		}},
		{"SearchFilesInline", func() error {
			_, err := SearchFilesInline(SearchOptions{
				BaseURL: opts.BaseURL, AuthID: opts.AuthID, Secret: opts.Secret,
				Datastore: opts.Datastore, HostPrefix: opts.BackupID,
				Query: "anything", Mode: "substring",
			})
			return err
		}},
	}
}

// With file restore disabled, every entry point refuses, and refuses with the
// POLICY error specifically.
//
// Pinning the error identity is what makes this a real assertion. Each of
// these functions would fail anyway against an unreachable PBS host, so "it
// returned an error" is satisfied by a network timeout and would pass with the
// gate deleted — the exact shape of vacuous test this repo has been bitten by
// before. errors.Is against ErrRestoreDisabled is the difference between
// "something went wrong" and "policy stopped it".
func TestEveryRestoreEntryPointRefusesWhenFileRestoreIsOff(t *testing.T) {
	forcePolicy(t, controlplane.Policy{FileRestore: false})

	for _, ep := range restoreEntryPoints() {
		t.Run(ep.name, func(t *testing.T) {
			err := ep.call()
			if err == nil {
				t.Fatalf("%s proceeded with file_restore disabled", ep.name)
			}
			if !errors.Is(err, ErrRestoreDisabled) {
				t.Fatalf("%s failed for the wrong reason: %v\n"+
					"(it must refuse ON POLICY — an unreachable-PBS error here would mean the gate is gone)",
					ep.name, err)
			}
		})
	}
}

// The converse, and it is not decoration: a gate that refuses unconditionally
// would pass the test above completely. This asserts the entry points do NOT
// return the policy refusal when policy permits — they get as far as trying to
// reach PBS, and fail on that instead.
func TestRestoreEntryPointsAreNotRefusedWhenPolicyPermits(t *testing.T) {
	forcePolicy(t, controlplane.Policy{FileRestore: true})

	for _, ep := range restoreEntryPoints() {
		t.Run(ep.name, func(t *testing.T) {
			// The host is unroutable, so an error is expected. What must NOT
			// happen is the policy refusal.
			if err := ep.call(); errors.Is(err, ErrRestoreDisabled) {
				t.Fatalf("%s refused on policy while file_restore was ENABLED", ep.name)
			}
		})
	}
}

// The sweep above is only as good as its table, so the table is checked
// against the source. A new exported *Inline entry point that nobody adds here
// would otherwise be covered by a test that looks comprehensive.
func TestTheEntryPointTableCoversEveryInlineEntryPoint(t *testing.T) {
	covered := map[string]bool{}
	for _, ep := range restoreEntryPoints() {
		covered[ep.name] = true
	}

	// Exported, restore-shaped, and reachable from a Wails binding.
	want := []string{
		"ListSnapshotsInline",
		"ListSnapshotContentsInline",
		"ReadSnapshotMetaInline",
		"RestoreSnapshotInline",
		"SearchFilesInline",
	}
	for _, fn := range want {
		if !covered[fn] {
			t.Errorf("%s is not in the entry-point table, so the policy sweep never calls it", fn)
		}
	}

	for _, spec := range []struct{ file, fn string }{
		{"restore_inline.go", "func ListSnapshotsInline("},
		{"restore_inline.go", "func ListSnapshotContentsInline("},
		{"restore_inline.go", "func ReadSnapshotMetaInline("},
		{"restore_inline.go", "func RestoreSnapshotInline("},
		{"restore_search.go", "func SearchFilesInline("},
	} {
		src := sourceOf(t, spec.file)
		if !strings.Contains(src, spec.fn) {
			t.Errorf("%s: %q is gone — the table above now names a function that "+
				"does not exist, and the sweep is testing nothing for it", spec.file, spec.fn)
		}
	}
}
