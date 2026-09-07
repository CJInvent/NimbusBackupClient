package main

import "fmt"

// Reporting this machine's credential-storage posture (NimbusControl
// V4-SECURITY-ROADMAP §6, wire contract in its docs/AGENT-API.md).
//
// WHY THIS IS WORTH A WIRE FIELD. The protector chain in secrets.go falls back
// on purpose: TPM, else DPAPI, else a file this agent can only obfuscate.
// Every one of those fallbacks is silent, because none of them may stop a
// machine backing up -- and that is exactly right for one machine and exactly
// wrong for a fleet. Silent per machine means an MSP with a hundred machines
// believes all hundred hold their secrets in a TPM, and nothing anywhere tells
// them the nine that do not. This reports what the machine actually got.
//
// It reports the level FALLEN BACK TO, never the one attempted. The attempt is
// already in this machine's own log; the server needs the outcome.

// credentialStorageLevel answers in the server's vocabulary: tpm | dpapi |
// plaintext | unavailable, or "" for "say nothing this cycle".
//
// "" and "unavailable" are different answers and the difference matters. An
// empty string omits the field, and the server keeps whatever it already knew
// -- the right behavior when this agent has nothing NEW to say.
// "unavailable" is a positive report that the key store could not be
// consulted, which is a machine whose backups are about to start refusing
// (backupKeyStorage returns Err for the same condition, and the gate refuses
// on it). Reporting the second as the first would hide the more urgent state
// behind the quieter one.
func credentialStorageLevel() string {
	_, protector, err := dekSource()
	if err != nil {
		return "unavailable"
	}

	switch protector {
	case "tpm", "dpapi", "plaintext":
		return protector
	default:
		// A protector this function does not recognize is a protector the
		// SERVER will drop, and reporting it as "unavailable" would state
		// something untrue about a machine whose store worked fine. Say
		// nothing, loudly: whoever added a protector to secrets.go without
		// adding it here gets a line in the log rather than a fleet quietly
		// reporting the wrong posture.
		writeWarnLog(fmt.Sprintf(
			"[Secrets] protector %q has no reporting level; the control plane will not be told what protects this machine",
			protector,
		))
		return ""
	}
}
