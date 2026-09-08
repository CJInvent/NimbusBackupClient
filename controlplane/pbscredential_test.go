package controlplane

import (
	"errors"
	"testing"
)

// The two 409s, which are the whole reason this file exists.
//
// /pbs-credential answers 409 for two situations that demand OPPOSITE
// responses from an agent -- register a key and retry, versus keep what you
// have and wait for an operator. Getting this wrong is not a cosmetic bug:
// one direction re-registers forever, the other throws away a working PBS
// configuration. The status alone cannot tell them apart, which is why the
// server sends a code.
func TestPBSCredential409IsSortedByCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{
			"key_required is the agent's own to fix",
			&httpError{status: 409, msg: "This machine has no registered public key", code: "key_required"},
			ErrKeyRequired,
		},
		{
			"not_provisioned is an operator's to fix",
			&httpError{status: 409, msg: "No PBS credential is provisioned for this machine", code: "not_provisioned"},
			ErrNotProvisioned,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := asPBSCredentialError(c.err)
			if !errors.Is(got, c.want) {
				t.Fatalf("got %v, want it to be %v", got, c.want)
			}
			// The opposite sentinel must NOT match, or errors.Is would be
			// satisfied by anything and the branch would be decorative.
			other := ErrKeyRequired
			if c.want == ErrKeyRequired {
				other = ErrNotProvisioned
			}
			if errors.Is(got, other) {
				t.Fatalf("%v also matched %v; the two sentinels are not distinct", got, other)
			}
		})
	}
}

// A code we have never heard of must not be guessed at. ErrNotProvisioned is
// the safe default precisely because its prescribed response is to change
// nothing -- a client that guessed ErrKeyRequired would re-register on every
// unfamiliar failure.
func TestUnknownCodeFallsBackToTheSafeDirection(t *testing.T) {
	got := asPBSCredentialError(&httpError{status: 409, msg: "something new", code: "some_future_code"})
	if !errors.Is(got, ErrNotProvisioned) {
		t.Fatalf("got %v, want ErrNotProvisioned", got)
	}
}

// The message fallback exists for one situation only -- a server older than
// the code field -- and must still get the consequential case right.
func TestMessageFallbackWhenAServerSendsNoCode(t *testing.T) {
	got := asPBSCredentialError(&httpError{
		status: 409,
		msg:    "This machine has no registered public key, so no secret can be sealed to it.",
	})
	if !errors.Is(got, ErrKeyRequired) {
		t.Fatalf("got %v, want ErrKeyRequired", got)
	}
	got = asPBSCredentialError(&httpError{status: 409, msg: "No PBS credential is provisioned for this machine"})
	if !errors.Is(got, ErrNotProvisioned) {
		t.Fatalf("got %v, want ErrNotProvisioned", got)
	}
}

// Anything that is not a 409 on this endpoint is somebody else's problem and
// must arrive unchanged. Translating a 429 into ErrNotProvisioned would tell
// an agent to stop asking about a limit that expires on its own.
func TestNon409ErrorsPassThroughUntouched(t *testing.T) {
	for _, status := range []int{401, 429, 500, 503} {
		in := &httpError{status: status, msg: "nope"}
		got := asPBSCredentialError(in)
		if got != error(in) {
			t.Fatalf("status %d: got %v, want the original error back", status, got)
		}
		if errors.Is(got, ErrNotProvisioned) || errors.Is(got, ErrKeyRequired) {
			t.Fatalf("status %d was translated into a P3 sentinel", status)
		}
	}
	if got := asPBSCredentialError(errors.New("dial tcp: refused")); got == nil {
		t.Fatal("a transport error was swallowed")
	}
}

// Complete() decides whether a target is safe to configure a machine from.
// Half a target points it at a real server with no datastore, which fails
// inside a backup rather than at the check-in that delivered it.
func TestOnlyAWholeTargetIsUsable(t *testing.T) {
	whole := PBSTarget{
		AuthID: "nimbus-clients@pbs!acme--fd-01-17", BaseURL: "https://pbs.example:8007",
		Datastore: "DS1", Namespace: "NimbusManaged/acme/fd-01-17",
	}
	if !whole.Complete() {
		t.Fatal("a fully-specified target was rejected")
	}
	// Namespace is deliberately NOT required: the root of a datastore is a
	// legitimate destination, and demanding one would refuse a valid target.
	noNS := whole
	noNS.Namespace = ""
	if !noNS.Complete() {
		t.Fatal("an empty namespace is the datastore root, not an incomplete target")
	}
	for _, blank := range []func(*PBSTarget){
		func(p *PBSTarget) { p.AuthID = "" },
		func(p *PBSTarget) { p.BaseURL = "" },
		func(p *PBSTarget) { p.Datastore = "" },
	} {
		bad := whole
		blank(&bad)
		if bad.Complete() {
			t.Fatalf("an incomplete target was accepted: %+v", bad)
		}
	}
	var nilTarget *PBSTarget
	if nilTarget.Complete() {
		t.Fatal("nil reported itself complete")
	}
}
