package main

import (
	"crypto/rand"
	"encoding/base64"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

// sealToThisMachine seals a payload exactly as the control plane does: with
// crypto_box_seal to the public key this machine has registered.
//
// The server's half is PHP's sodium_crypto_box_seal; this is Go's
// nacl/box.SealAnonymous. They are the same construction -- an ephemeral
// keypair per message, nonce = blake2b(ephemeral_pk || recipient_pk), 48 bytes
// of overhead -- and the two implementations were checked against each other
// directly (a payload sealed by the server's PHP opened by this client's Go,
// 2026-09-07). TestSealedBoxOverheadMatchesLibsodium below pins the part of
// that agreement a test can see without a PHP interpreter, so a change to
// either library that broke the format would fail here rather than on a
// customer's first backup.
func sealToThisMachine(t *testing.T, plain []byte) (sealedB64 string, keyID string) {
	t.Helper()
	keyID, pub, err := loadOrCreateAgentKey()
	if err != nil {
		t.Fatalf("agent key: %v", err)
	}
	var pubArr [32]byte
	copy(pubArr[:], pub)
	sealed, err := box.SealAnonymous(nil, plain, &pubArr, rand.Reader)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sealed), keyID
}
