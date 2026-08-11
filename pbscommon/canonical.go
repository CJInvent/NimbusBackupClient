package pbscommon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Canonical JSON, as PBS defines it.
//
// A manifest signature is an HMAC over a CANONICAL rendering of the manifest,
// not over whatever bytes we happened to serialise. Two JSON documents that a
// parser considers identical produce different signatures if their key order
// or spacing differs, so "canonical" here is not a style preference — it is
// part of the wire format, and a client that gets it wrong writes snapshots
// whose signature `proxmox-backup-client` rejects at restore time.
//
// This is a deliberately literal port of
// proxmox_serde::json::write_canonical_json, read from
// github.com/proxmox/proxmox-rs, proxmox-serde/src/json.rs. Its rules:
//
//   - object keys sorted, byte-wise (Rust `sort_unstable` over `&str`; Go's
//     sort.Strings is the same ordering)
//   - no whitespace anywhere
//   - a null ANYWHERE is an ERROR, not an emitted "null". Upstream bails, and
//     reproducing that is the point: if a null reaches this function we have
//     failed to strip a field that PBS strips, and signing the document would
//     produce a signature nothing can verify. Failing here is loud; emitting
//     "null" would be silent and only surface at a restore.
//
// Go's encoding/json is NOT used to render the leaves. It differs from
// serde_json in ways that are invisible until they are not: it escapes
// <, > and & by default, escapes U+2028/U+2029 always, and writes \u0008 and
// \u000c where serde writes \b and \f. None of those characters occur in a
// manifest today — which is exactly why a mismatch would sit undetected until
// the first customer with an unusual filename.

// canonicalNumber rejects anything that is not an integer literal.
//
// PBS manifests contain no floating-point numbers: sizes, times and counts are
// all integers. Rather than guess whether Go and Rust would render some future
// float identically, refuse it. A signature computed over a number we rendered
// differently from upstream is worse than a hard error, because it verifies
// against us and against nothing else.
func canonicalNumber(n json.Number) error {
	if strings.ContainsAny(n.String(), ".eE") {
		return fmt.Errorf("canonical json: non-integer number %q (PBS manifests hold only integers)", n.String())
	}
	return nil
}

// writeCanonicalString escapes exactly as serde_json does: quote, backslash and
// the C0 control characters, with \b \t \n \f \r spelled out and every other
// control character as \u00xx. Nothing else is escaped — non-ASCII passes
// through as UTF-8.
func writeCanonicalString(out *bytes.Buffer, s string) error {
	if !utf8.ValidString(s) {
		return errors.New("canonical json: string is not valid UTF-8")
	}
	out.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			out.WriteString(`\"`)
		case c == '\\':
			out.WriteString(`\\`)
		case c == '\b':
			out.WriteString(`\b`)
		case c == '\f':
			out.WriteString(`\f`)
		case c == '\n':
			out.WriteString(`\n`)
		case c == '\r':
			out.WriteString(`\r`)
		case c == '\t':
			out.WriteString(`\t`)
		case c < 0x20:
			fmt.Fprintf(out, `\u%04x`, c)
		default:
			out.WriteByte(c)
		}
	}
	out.WriteByte('"')
	return nil
}

func writeCanonicalValue(out *bytes.Buffer, v any) error {
	switch val := v.(type) {
	case nil:
		// Upstream: `Value::Null => bail!("got unexpected null value")`.
		return errors.New("canonical json: unexpected null value")
	case bool:
		if val {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case json.Number:
		if err := canonicalNumber(val); err != nil {
			return err
		}
		out.WriteString(val.String())
	case string:
		return writeCanonicalString(out, val)
	case []any:
		out.WriteByte('[')
		for i, item := range val {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonicalValue(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys) // byte-wise, matching Rust's sort_unstable over &str
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonicalString(out, k); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := writeCanonicalValue(out, val[k]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("canonical json: unsupported value of type %T", v)
	}
	return nil
}

// ToCanonicalJSON renders an already-decoded JSON document canonically.
//
// The input must have been decoded with a Decoder in UseNumber mode — see
// DecodeJSONDocument. Plain json.Unmarshal turns every number into a float64,
// so 1593179765 comes back as 1.593179765e9 and the signature silently stops
// matching the one PBS computes.
func ToCanonicalJSON(doc any) ([]byte, error) {
	var out bytes.Buffer
	if err := writeCanonicalValue(&out, doc); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// DecodeJSONDocument parses JSON into the generic shape ToCanonicalJSON wants,
// preserving numeric literals exactly as written.
func DecodeJSONDocument(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	return doc, nil
}
