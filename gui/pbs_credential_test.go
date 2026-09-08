package main

import (
	"testing"

	"controlplane"
)

// applyPBSTargetFields writes only on a real change, and the reason is not
// tidiness: a check-in lands roughly every two minutes, so a function that
// reported "changed" every time would rewrite the config file about 720 times
// a day to store identical bytes, and bury every genuine provisioning event in
// a log nobody can read.
func TestTheConfigIsRewrittenOnlyWhenSomethingActuallyChanged(t *testing.T) {
	target := controlplane.PBSTarget{
		AuthID: "nimbus-clients@pbs!acme--fd-01-17", BaseURL: "https://pbs.example:8007",
		Datastore: "DS1", Namespace: "NimbusManaged/acme/fd-01-17", Fingerprint: "AB:CD",
	}

	cfg := &Config{}
	if !applyPBSTargetFields(cfg, &target) {
		t.Fatal("the first target ever seen reported no change")
	}
	if cfg.BaseURL != target.BaseURL || cfg.AuthID != target.AuthID ||
		cfg.Datastore != target.Datastore || cfg.Namespace != target.Namespace ||
		cfg.CertFingerprint != target.Fingerprint {
		t.Fatalf("the target was not applied in full: %+v", cfg)
	}
	if applyPBSTargetFields(cfg, &target) {
		t.Fatal("re-applying an identical target reported a change")
	}

	// Every field independently, because a comparison that omits one is a
	// machine that silently keeps backing up to the old one.
	for name, mutate := range map[string]func(*controlplane.PBSTarget){
		"base URL":    func(p *controlplane.PBSTarget) { p.BaseURL = "https://other:8007" },
		"auth id":     func(p *controlplane.PBSTarget) { p.AuthID = "nimbus-clients@pbs!acme--fd-01-18" },
		"datastore":   func(p *controlplane.PBSTarget) { p.Datastore = "DS2" },
		"namespace":   func(p *controlplane.PBSTarget) { p.Namespace = "NimbusManaged/acme/other" },
		"fingerprint": func(p *controlplane.PBSTarget) { p.Fingerprint = "EF:01" },
	} {
		fresh := &Config{}
		applyPBSTargetFields(fresh, &target)
		moved := target
		mutate(&moved)
		if !applyPBSTargetFields(fresh, &moved) {
			t.Fatalf("a changed %s was not noticed", name)
		}
	}
}

// The secret survives a target change on purpose. Blanking it first would
// leave a window in which this machine can reach PBS and cannot authenticate
// to it; a wrong-but-present secret fails the same way an absent one does, and
// fails without a gap.
func TestApplyingATargetDoesNotBlankTheSecretItIsAboutToReplace(t *testing.T) {
	cfg := &Config{
		AuthID: "nimbus-clients@pbs!acme--fd-01-17", BaseURL: "https://pbs.example:8007",
		Datastore: "DS1", Secret: "the-old-token",
	}
	applyPBSTargetFields(cfg, &controlplane.PBSTarget{
		AuthID: "nimbus-clients@pbs!acme--fd-01-18", BaseURL: "https://pbs.example:8007",
		Datastore: "DS1",
	})
	if cfg.Secret != "the-old-token" {
		t.Fatalf("the secret was cleared, leaving an authentication gap: %q", cfg.Secret)
	}
}

// A sealed credential that opens to nothing must be an error, never an empty
// string. A blank token written into the config is indistinguishable from
// "never provisioned" on the next cycle, so the machine would refetch forever
// against a 3/hour limit while looking, in the config, entirely healthy.
func TestAnUnopenableCredentialIsAnErrorAndNotAnEmptySecret(t *testing.T) {
	for name, cred := range map[string]*controlplane.PBSCredential{
		"nothing delivered":  nil,
		"no sealed material": {},
		"not base64":         {SecretSealedB64: "!!! not base64 !!!"},
	} {
		got, err := openSealedPBSSecret(cred)
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		if got != "" {
			t.Fatalf("%s: returned a secret alongside its error: %q", name, got)
		}
	}
}
