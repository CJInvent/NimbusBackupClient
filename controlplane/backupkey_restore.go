package controlplane

// The restore key mode (V4-SPEC §18, phase G).
//
// A SEPARATE FILE FROM backupkey.go, which is phase F's: this is a mode for
// READING a snapshot rather than writing one, and it exists for the audit's
// sake rather than the gate's.
//
// A machine with no durable key storage holds nothing at rest by design, so it
// has to ask the server for the key in order to restore. That request releases
// key material and is audited like any other release — but no backup run
// follows it, and none ever will, which is exactly the shape a stolen agent
// token has.
//
// THE MODE IS NOT AN EXEMPTION. The obvious server-side implementation is to
// drop restore releases from the unmatched-releases report, since they are
// unmatched by construction; that would be a hole the size of the audit,
// because the mode is CLIENT-ASSERTED and an attacker would simply claim it.
// Restore releases are still flagged. What the mode buys is a legible report:
// an operator can tell "a restore happened here" from "something took the key
// and never backed anything up", instead of the two arriving as the same row
// and the first teaching them to ignore the second.
//
// Server side: migration 022_key_release_restore_mode.sql, and the mode shares
// the ephemeral rate ceiling — a recovery opens several snapshots in a row
// while hunting for the right one, and the durable limit would stop an
// operator halfway through one.
const KeyModeRestore = "restore"
