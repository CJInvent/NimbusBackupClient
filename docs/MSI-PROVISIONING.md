# MSI Provisioning Contract — v1

How a preconfigured NimbusBackup installer carries an organization's identity,
so a technician images a machine and walks away instead of typing a URL and a
token into a GUI on every endpoint.

This document is the **interface between two repositories**. NimbusControl
generates profiles; NimbusBackupClient consumes them. Neither side may change
the shape below without bumping `profile_version` in both.

- Client implementation: `controlplane/profile.go` (parse/validate),
  `gui/provisioning.go` (consume/apply/destroy)
- Contract version: **1** (`controlplane.ProfileVersion`)

---

## The flow

1. An org admin downloads an install profile from their NimbusControl org page.
2. The MSI build embeds that profile (see *Building a preconfigured MSI*).
3. The installer writes it to `C:\ProgramData\NimbusBackup\provisioning.json`.
4. On its next start the **service** reads it, applies it, enrolls, and
   destroys the file.
5. The agent appears in the org's fleet with no further interaction.

The GUI is not involved at any point. Config writes belong to the service
(dev rule 2), and this is a config write carrying a credential.

## The profile

```json
{
  "profile_version": 1,
  "org_name": "Acme Corp",
  "control_server_url": "https://control.example.com",
  "control_cert_fp": "aabbcc…",
  "enroll_token": "one-time-org-enrollment-token",
  "default_backup_mode": "directory",
  "issued_at": "2026-07-19T12:00:00Z",
  "issued_by": "nimbuscontrol/0.1.2"
}
```

| Field | Required | Rules |
|---|---|---|
| `profile_version` | yes | Must equal 1. A newer version is **refused**, not best-effort parsed. |
| `control_server_url` | yes | Must parse, must be `https`, must have a host. |
| `enroll_token` | yes | Non-empty. One-time, org-scoped, revocable server-side. |
| `control_cert_fp` | no | SHA-256 leaf fingerprint, 64 hex chars. Colon grouping and any case accepted; normalized on ingest. Omit when the server uses a publicly trusted certificate. |
| `org_name` | no | Diagnostics and logs only. Never authoritative. |
| `default_backup_mode` | no | `directory` or `machine`. Seeds a fresh machine's initial choice; never overrides one already made. |
| `issued_at` | no | RFC 3339. Logged, so a stale rollout is visible. |
| `issued_by` | no | Generator identification, for support. |
| `signature` | no | **Reserved. Not verified.** See *Integrity*. |

**Unknown fields are a hard error.** A field this agent does not understand
may be load-bearing on the server side, and half-applying a security profile
is worse than refusing it. That means adding a field to this contract is a
breaking change requiring a version bump — deliberately.

## Client behavior, and why

Two rules govern ingestion. Both exist because violating them fails silently.

**An already-enrolled agent is never re-pointed.** An in-place upgrade
re-delivers the same profile. A machine that has been backing up for a year
must not be silently moved to another org, nor have its identity reset. If
`control_agent_id` is set — or a *different* control server is already
configured — the profile is discarded, unapplied, with the reason logged.

**The token is destroyed on every path.** Applied, refused, malformed, or
discarded: the file is overwritten and unlinked before the function returns.
It is a one-time bearer credential, and leaving it on disk "to retry later"
converts a provisioning artifact into a permanent one (dev rule 10). If
removal fails, that is logged loudly as a standing risk with the path to
delete by hand.

Enrollment itself is unchanged: the existing one-time-token path in
`StartControlPlane` runs immediately afterwards, and wipes
`control_enroll_token` from config once the agent has an identity.

## Integrity — read this before trusting a profile

The profile carries a bearer credential and names the server the agent will
obey. Its protections are:

- **The MSI's Authenticode signature is the integrity boundary.** Anyone who
  can rewrite the profile inside an **unsigned** installer can point agents at
  a control server of their choosing. Signing is Phase 4; until then, treat a
  preconfigured MSI as you would any unsigned installer and distribute it over
  a channel you trust.
- **Pinning travels with the URL.** A profile that names a control server also
  names its certificate fingerprint, so altering the URL alone does not yield
  a working man-in-the-middle.
- **The token is one-time, org-scoped, and revocable.** A leaked profile costs
  the org one unauthorized agent registration, visible in the fleet list, and
  is revoked by expiring the token server-side.
- **`signature` is reserved and not verified.** Verifying it would need a
  trust anchor the agent possesses *before* it has ever contacted the server —
  the same distribution problem the installer signature already solves. The
  field exists so adding real verification later is not a version bump; it is
  not a control today and must not be presented as one.

Distribution guidance for NimbusControl: profiles are per-org secrets. Serve
them over authenticated HTTPS, log downloads, and give them a short
`issued_at` shelf life.

## Building a preconfigured MSI

```
scripts/build-provisioned-msi.ps1 -Profile acme.json -Version 0.2.150
```

The script validates the profile against this contract *before* building —
a broken profile should fail at build time on the MSP's workstation, not at
first boot on a customer endpoint. It then stages the profile as a WiX payload
installed to `C:\ProgramData\NimbusBackup\provisioning.json`.

The stock MSI is unchanged: with no profile the installer behaves exactly as
it does today, and the agent starts standalone.

## Server-side obligations (NimbusControl Phase 6)

1. Generate profiles per org with a one-time enrollment token.
2. Emit `profile_version: 1` and nothing outside the table above.
3. Serve `control_cert_fp` when the org's control server uses a private CA or
   a self-signed certificate.
4. Show download provenance on the org page — who generated a profile and
   when — because it is a credential.
5. Accept enrollment from an agent whose only prior contact is this token.

## Open items

- `signature` verification (needs an org trust anchor; see *Integrity*).
- Authenticode signing of the produced MSI (Phase 4).
- Reporting break-glass activations upstream — the inventory field lands with
  the same contract work.
