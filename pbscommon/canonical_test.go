package pbscommon

import (
	"strings"
	"testing"
)

func canon(t *testing.T, in string) string {
	t.Helper()
	doc, err := DecodeJSONDocument([]byte(in))
	if err != nil {
		t.Fatalf("decode %s: %v", in, err)
	}
	out, err := ToCanonicalJSON(doc)
	if err != nil {
		t.Fatalf("canonicalise %s: %v", in, err)
	}
	return string(out)
}

func canonErr(t *testing.T, in string) error {
	t.Helper()
	doc, err := DecodeJSONDocument([]byte(in))
	if err != nil {
		t.Fatalf("decode %s: %v", in, err)
	}
	_, err = ToCanonicalJSON(doc)
	return err
}

func TestCanonicalOrdersKeysAndStripsSpace(t *testing.T) {
	got := canon(t, `{ "b" : 2 , "a" : 1 , "C" : 3 }`)
	// Byte-wise ordering, so uppercase sorts before lowercase — that is Rust's
	// sort_unstable over &str, not a locale-aware or case-insensitive sort.
	if want := `{"C":3,"a":1,"b":2}`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestCanonicalPreservesArrayOrder(t *testing.T) {
	// Arrays are ordered data; only OBJECT keys are sorted. Sorting an array
	// would silently reorder a manifest's file list.
	if got, want := canon(t, `[3,1,2]`), `[3,1,2]`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestCanonicalKeepsIntegersExact(t *testing.T) {
	// The failure this guards: json.Unmarshal without UseNumber turns
	// 1593179765 into a float64 and renders it 1.593179765e+09, which signs
	// cleanly and verifies nowhere.
	got := canon(t, `{"backup-time":1593179765}`)
	if want := `{"backup-time":1593179765}`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	// Check the VALUE, not the whole document: the key "backup-time" contains
	// an 'e', so scanning the rendered object for exponent characters fails on
	// correct output. (It did, the first time this ran.)
	value := strings.TrimSuffix(strings.SplitN(got, ":", 2)[1], "}")
	if strings.ContainsAny(value, "eE.") {
		t.Fatalf("integer rendered in exponent form: %s", value)
	}
}

func TestCanonicalRejectsNull(t *testing.T) {
	// Upstream bails on null. So must we: a null here means a field PBS strips
	// was not stripped, and emitting "null" would produce a signature that
	// verifies against nothing.
	if err := canonErr(t, `{"signature":null}`); err == nil {
		t.Fatal("a null value was accepted")
	}
	if err := canonErr(t, `{"files":[null]}`); err == nil {
		t.Fatal("a null inside an array was accepted")
	}
}

func TestCanonicalRejectsFloats(t *testing.T) {
	if err := canonErr(t, `{"size":1.5}`); err == nil {
		t.Fatal("a float was accepted")
	}
	if err := canonErr(t, `{"size":1e3}`); err == nil {
		t.Fatal("an exponent literal was accepted")
	}
}

// String escaping must match serde_json, not Go's encoding/json.
func TestCanonicalStringEscaping(t *testing.T) {
	cases := []struct{ in, want string }{
		// Go's encoder escapes these three as \u003c etc. by default; serde
		// does not. A filename containing an ampersand is not exotic.
		{`{"k":"a<b>c&d"}`, `{"k":"a<b>c&d"}`},
		// serde spells these two out; Go writes \u0008 and \u000c.
		{"{\"k\":\"\\b\\f\"}", `{"k":"\b\f"}`},
		{"{\"k\":\"\\n\\r\\t\"}", `{"k":"\n\r\t"}`},
		{`{"k":"quote\" back\\slash"}`, `{"k":"quote\" back\\slash"}`},
		// Other C0 controls as lowercase \u00xx.
		{`{"k":"\u0001"}`, `{"k":"\u0001"}`},
		// Non-ASCII passes through as UTF-8, unescaped.
		{`{"k":"Grüße"}`, `{"k":"Grüße"}`},
	}
	for _, c := range cases {
		if got := canon(t, c.in); got != c.want {
			t.Fatalf("input %s: got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestCanonicalEscapesKeysToo(t *testing.T) {
	if got, want := canon(t, `{"a\"b":1}`), `{"a\"b":1}`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestCanonicalEmptyContainers(t *testing.T) {
	if got, want := canon(t, `{"a":{},"b":[]}`), `{"a":{},"b":[]}`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
