//go:build windows

package main

// S7 — secret-store smoke on real Windows (ARCHITECTURE.md Part III, Phase 1).
//
// Exercises the encv1 DEK chain against the actual Windows crypto APIs, which
// is the only place it can be proven: the Linux jobs compile this code but
// never run DPAPI. CI runners have no TPM, so the protector that must appear
// here is dpapi — and the assertion that matters for dev rule 11 is that it is
// NOT "plaintext". A silent fall through to plaintext on real Windows would
// mean stored PBS credentials are merely obfuscated, and every other test in
// the tree would still pass.
//
// Every case isolates ProgramData into a temp dir (getConfigDir reads that env
// var), so nothing touches C:\ProgramData\NimbusBackup on the runner.

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// resetDEKCache clears the process-wide DEK cache so the next getDEK() re-reads
// master.key from the (freshly isolated) config dir.
func resetDEKCache() {
	dekMu.Lock()
	dekCached, dekProtector, dekErr = nil, "", nil
	dekMu.Unlock()
}

// isolateConfigDir points getConfigDir() at a temp directory for one test.
func isolateConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)
	resetDEKCache()
	t.Cleanup(resetDEKCache)
	return dir
}

func readMasterKey(t *testing.T) masterKeyFile {
	t.Helper()
	path, err := masterKeyPath()
	if err != nil {
		t.Fatalf("masterKeyPath: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("master key not written to %s: %v", path, err)
	}
	var mk masterKeyFile
	if err := json.Unmarshal(data, &mk); err != nil {
		t.Fatalf("master key is not valid JSON: %v", err)
	}
	return mk
}

func TestSecretStoreRoundTripWindows(t *testing.T) {
	isolateConfigDir(t)

	const plain = "pbs-api-token-secret"
	sealed := encryptSecret(plain)

	if !strings.HasPrefix(sealed, secretPrefixV1) {
		t.Fatalf("sealed value lacks the %q prefix: %q", secretPrefixV1, sealed)
	}
	if strings.Contains(sealed, plain) {
		t.Fatal("sealed value contains the plaintext")
	}
	if !isEncryptedSecret(sealed) {
		t.Error("isEncryptedSecret said no to a sealed value")
	}
	if isEncryptedSecret(plain) {
		t.Error("isEncryptedSecret said yes to a plaintext value")
	}
	if got := decryptSecret(sealed); got != plain {
		t.Fatalf("round trip = %q, want %q", got, plain)
	}

	// Sealing is nondeterministic (fresh nonce per call) but both open.
	if again := encryptSecret(plain); again == sealed {
		t.Error("two seals of the same plaintext were identical — nonce reuse")
	} else if got := decryptSecret(again); got != plain {
		t.Errorf("second seal round trip = %q, want %q", got, plain)
	}

	// Pass-through contracts: empty stays empty, already-sealed is not
	// double-wrapped, legacy plaintext reads back unchanged.
	if got := encryptSecret(""); got != "" {
		t.Errorf("encryptSecret(\"\") = %q, want empty", got)
	}
	if got := encryptSecret(sealed); got != sealed {
		t.Error("an already-sealed value was re-sealed")
	}
	if got := decryptSecret("legacy-plaintext"); got != "legacy-plaintext" {
		t.Errorf("legacy plaintext = %q, want unchanged", got)
	}
}

// The protector actually used on real Windows must be a hardware- or
// OS-backed one. Plaintext here means the chain silently degraded.
func TestSecretStoreProtectorIsNotPlaintextWindows(t *testing.T) {
	isolateConfigDir(t)

	if got := decryptSecret(encryptSecret("x")); got != "x" {
		t.Fatalf("round trip failed: %q", got)
	}

	mk := readMasterKey(t)
	switch mk.Protector {
	case "dpapi", "tpm":
		t.Logf("master key protector: %s", mk.Protector)
	case "plaintext":
		t.Fatal("master key fell back to the PLAINTEXT protector on Windows — " +
			"stored secrets would be obfuscated, not protected (dev rule 11)")
	default:
		t.Fatalf("unknown protector %q", mk.Protector)
	}
	if mk.Blob == "" {
		t.Error("master key blob is empty")
	}

	// The DEK the rest of the app uses reports the same protector.
	if _, protector, err := getDEK(); err != nil {
		t.Errorf("getDEK: %v", err)
	} else if protector != mk.Protector {
		t.Errorf("getDEK protector %q != on-disk %q", protector, mk.Protector)
	}
}

// A secret that cannot be opened must degrade to "re-enter" (empty), never to
// a crash and never to a bogus plaintext.
func TestSecretStoreCorruptDegradesToReentryWindows(t *testing.T) {
	isolateConfigDir(t)

	sealed := encryptSecret("original-secret")

	for name, bad := range map[string]string{
		"not base64":  secretPrefixV1 + "!!!not-base64!!!",
		"truncated":   secretPrefixV1 + base64.StdEncoding.EncodeToString([]byte{1, 2, 3}),
		"tag flipped": flipLastByte(sealed),
	} {
		if got := decryptSecret(bad); got != "" {
			t.Errorf("%s: decryptSecret = %q, want empty (re-enter)", name, got)
		}
	}

	// A bare prefix with no payload is NOT a sealed value: isEncryptedSecret
	// requires content after the prefix, so it passes through as legacy
	// plaintext rather than being reported as an unrecoverable secret.
	if got := decryptSecret(secretPrefixV1); got != secretPrefixV1 {
		t.Errorf("bare prefix = %q, want it passed through unchanged", got)
	}

	// A foreign master key (machine replaced, key lost) must also degrade to
	// re-entry rather than surfacing garbage.
	path, err := masterKeyPath()
	if err != nil {
		t.Fatalf("masterKeyPath: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove master key: %v", err)
	}
	resetDEKCache()
	if got := decryptSecret(sealed); got != "" {
		t.Errorf("secret opened with a different master key = %q, want empty", got)
	}
}

// Documented behavior: a weaker protector is upgraded in place on load, the
// DEK itself is unchanged, and secrets sealed before the upgrade still open.
func TestSecretStoreProtectorUpgradeWindows(t *testing.T) {
	isolateConfigDir(t)

	path, err := masterKeyPath()
	if err != nil {
		t.Fatalf("masterKeyPath: %v", err)
	}

	// Plant a plaintext-protected master key, as an older build would leave.
	dek := make([]byte, 32)
	for i := range dek {
		dek[i] = byte(i + 1)
	}
	if err := writeMasterKey(path, "plaintext", dek); err != nil {
		t.Fatalf("plant plaintext master key: %v", err)
	}
	resetDEKCache()

	// Sealing forces a load, which must upgrade the wrapping.
	sealed := encryptSecret("survives-the-upgrade")

	loaded, protector, err := getDEK()
	if err != nil {
		t.Fatalf("getDEK after upgrade: %v", err)
	}
	if protector == "plaintext" {
		t.Fatal("plaintext master key was not upgraded on load")
	}
	if string(loaded) != string(dek) {
		t.Fatal("the DEK changed during a protector upgrade — every stored secret would be lost")
	}
	if mk := readMasterKey(t); mk.Protector == "plaintext" {
		t.Error("upgrade was not persisted to master.key")
	}

	// The pre-upgrade ciphertext still opens: only the wrapping changed.
	resetDEKCache()
	if got := decryptSecret(sealed); got != "survives-the-upgrade" {
		t.Errorf("secret sealed before the upgrade = %q, want it to still open", got)
	}
}

func flipLastByte(sealed string) string {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, secretPrefixV1))
	if err != nil || len(raw) == 0 {
		return sealed
	}
	raw[len(raw)-1] ^= 0xFF
	return secretPrefixV1 + base64.StdEncoding.EncodeToString(raw)
}
