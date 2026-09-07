package main

import (
	"errors"
	"testing"
)

// The reporting level and the durable/ephemeral decision read the SAME
// protector, so these tests use the same injection the key-store suite does
// (withProtector / withBrokenDEK): a Linux runner has neither DPAPI nor a TPM,
// so without it every branch but "plaintext" would be unreachable and the
// whole function would sit untested behind one early return.

func TestCredentialStorageLevelReportsWhatTheMachineGot(t *testing.T) {
	for _, protector := range []string{"tpm", "dpapi", "plaintext"} {
		withProtector(t, protector)
		if got := credentialStorageLevel(); got != protector {
			t.Fatalf("protector %q reported as %q", protector, got)
		}
	}
}

// A store that cannot be CONSULTED is not the same as a weak one, and it is
// the more urgent of the two: the backup-key gate refuses on this condition.
// Reporting it as "" (say nothing) would leave the server showing the last
// good posture for a machine whose backups are about to start failing.
func TestCredentialStorageLevelDistinguishesUnreadableFromWeak(t *testing.T) {
	withBrokenDEK(t, errors.New("master key unavailable"))
	if got := credentialStorageLevel(); got != "unavailable" {
		t.Fatalf("an unreadable key store reported as %q, want \"unavailable\"", got)
	}
}

// A protector added to secrets.go without being added here must produce
// silence, not a wrong answer: the server drops what it cannot label, and
// "unavailable" would state something untrue about a machine whose store
// worked perfectly.
func TestCredentialStorageLevelSaysNothingRatherThanSomethingWrong(t *testing.T) {
	withProtector(t, "some-future-protector")
	if got := credentialStorageLevel(); got != "" {
		t.Fatalf("unknown protector reported as %q, want the field omitted", got)
	}
}
