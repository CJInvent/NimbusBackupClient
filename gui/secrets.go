package main

// Phase 2 of the zero-trust plan: secrets at rest.
//
// Stored secrets (PBS API token secrets, SMTP password) are envelope-encrypted:
// each value is sealed with AES-256-GCM under a random 256-bit data encryption
// key (DEK), and the DEK itself is protected by the strongest available
// "protector" on the machine:
//
//	dpapi     - Windows DPAPI, machine scope (CryptProtectData). Available on
//	            every Windows box; lets the LocalSystem service and a user GUI
//	            in standalone mode share the same key material.
//	plaintext - last-resort fallback (non-Windows, or DPAPI failure). The DEK
//	            is stored raw next to the config; this is obfuscation only and
//	            is logged loudly. Some backup is better than no backup.
//
// Phase 3 adds a "tpm" protector for the DEK behind the same interface; the
// per-secret format does not change, so upgrading protectors never requires
// re-encrypting secrets — only re-wrapping the DEK.
//
// Encrypted values carry the prefix "encv1:"; anything else is treated as
// legacy plaintext and migrated on the next load/save cycle. Decryption
// failures (e.g. key file deleted, moved to another machine) degrade to an
// empty secret so the user re-enters it — never a crash, never a stuck config.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const secretPrefixV1 = "encv1:"

// masterKeyFile is the on-disk wrapped DEK, next to config.json.
type masterKeyFile struct {
	Protector string `json:"protector"` // "dpapi" | "plaintext"
	Blob      string `json:"blob"`      // base64 of the (wrapped) DEK
}

var (
	dekOnce      sync.Once
	dekCached    []byte
	dekProtector string
	dekErr       error
)

// getDEK returns the machine's data encryption key, creating and persisting it
// on first use. Cached for the process lifetime.
func getDEK() ([]byte, string, error) {
	dekOnce.Do(func() {
		dekCached, dekProtector, dekErr = loadOrCreateDEK()
	})
	return dekCached, dekProtector, dekErr
}

func masterKeyPath() (string, error) {
	dir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "master.key"), nil
}

func loadOrCreateDEK() ([]byte, string, error) {
	path, err := masterKeyPath()
	if err != nil {
		return nil, "", err
	}

	if data, err := os.ReadFile(path); err == nil {
		var mk masterKeyFile
		if err := json.Unmarshal(data, &mk); err != nil {
			return nil, "", fmt.Errorf("master key file corrupt: %w", err)
		}
		blob, err := base64.StdEncoding.DecodeString(mk.Blob)
		if err != nil {
			return nil, "", fmt.Errorf("master key blob corrupt: %w", err)
		}
		switch mk.Protector {
		case "dpapi":
			dek, err := dpapiUnprotect(blob)
			if err != nil {
				return nil, "", fmt.Errorf("dpapi unwrap of master key failed: %w", err)
			}
			return dek, mk.Protector, nil
		case "plaintext":
			writeDebugLog("[Secrets] WARNING: master key is stored with the PLAINTEXT protector - stored secrets are only obfuscated, not protected")
			return blob, mk.Protector, nil
		default:
			return nil, "", fmt.Errorf("unknown master key protector %q", mk.Protector)
		}
	}

	// No key yet: create one under the strongest available protector.
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, "", fmt.Errorf("generating master key failed: %w", err)
	}

	protector := "dpapi"
	blob, err := dpapiProtect(dek)
	if err != nil {
		writeDebugLog(fmt.Sprintf("[Secrets] WARNING: DPAPI unavailable (%v) - falling back to PLAINTEXT protector; stored secrets will only be obfuscated", err))
		protector = "plaintext"
		blob = dek
	} else {
		writeDebugLog("[Secrets] Master key created under the DPAPI (machine) protector")
	}

	mk := masterKeyFile{Protector: protector, Blob: base64.StdEncoding.EncodeToString(blob)}
	data, err := json.MarshalIndent(mk, "", "  ")
	if err != nil {
		return nil, "", err
	}
	if err := atomicWriteFile(path, data, 0600); err != nil {
		return nil, "", fmt.Errorf("writing master key failed: %w", err)
	}
	return dek, protector, nil
}

// encryptSecret seals a secret for storage. Empty values pass through, already
// encrypted values pass through, and on any failure the plaintext is returned
// unchanged (with a logged warning) so a save never destroys a credential.
func encryptSecret(plain string) string {
	if plain == "" || isEncryptedSecret(plain) {
		return plain
	}
	dek, _, err := getDEK()
	if err != nil {
		writeDebugLog(fmt.Sprintf("[Secrets] WARNING: no master key (%v) - storing secret unencrypted", err))
		return plain
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		writeDebugLog(fmt.Sprintf("[Secrets] cipher init failed: %v - storing secret unencrypted", err))
		return plain
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		writeDebugLog(fmt.Sprintf("[Secrets] gcm init failed: %v - storing secret unencrypted", err))
		return plain
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		writeDebugLog(fmt.Sprintf("[Secrets] nonce generation failed: %v - storing secret unencrypted", err))
		return plain
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return secretPrefixV1 + base64.StdEncoding.EncodeToString(sealed)
}

// decryptSecret opens a stored secret. Legacy plaintext values pass through.
// A value that carries the encv1 prefix but cannot be opened yields "" (the
// user must re-enter it) rather than an error, so a lost/foreign master key
// degrades to re-entry instead of a broken application.
func decryptSecret(v string) string {
	if !isEncryptedSecret(v) {
		return v
	}
	raw, err := base64.StdEncoding.DecodeString(v[len(secretPrefixV1):])
	if err != nil {
		writeDebugLog("[Secrets] ERROR: stored secret is corrupt - it must be re-entered")
		return ""
	}
	dek, _, err := getDEK()
	if err != nil {
		writeDebugLog(fmt.Sprintf("[Secrets] ERROR: cannot load master key (%v) - stored secret must be re-entered", err))
		return ""
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		writeDebugLog(fmt.Sprintf("[Secrets] ERROR: cipher init failed (%v)", err))
		return ""
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		writeDebugLog(fmt.Sprintf("[Secrets] ERROR: gcm init failed (%v)", err))
		return ""
	}
	if len(raw) < gcm.NonceSize() {
		writeDebugLog("[Secrets] ERROR: stored secret is truncated - it must be re-entered")
		return ""
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		writeDebugLog("[Secrets] ERROR: stored secret cannot be decrypted with this machine's master key - it must be re-entered")
		return ""
	}
	return string(plain)
}

func isEncryptedSecret(v string) bool {
	return len(v) > len(secretPrefixV1) && v[:len(secretPrefixV1)] == secretPrefixV1
}
