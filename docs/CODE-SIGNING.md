# Code Signing

Nimbus Backup ships **unsigned** today. This document records why that matters,
which provider actually fits this product, and the split between what has to be
done by a human with a company identity and what CI already handles.

## Why it matters here more than for most software

Three things compound:

1. **SmartScreen.** An unsigned installer downloaded from the internet gets an
   "unrecognized app" interstitial. For an MSP handing a customer an installer,
   that is a support call every time.
2. **AV heuristics.** An unsigned, unversioned binary that takes VSS snapshots,
   reads raw disks and installs a LocalSystem service looks exactly like
   something malicious. The `.syso` version metadata (asserted by smoke S8)
   exists to mitigate this; a signature is the rest of it.
3. **Provisioning made it structural.** A preconfigured MSI carries a live org
   enrollment token (`docs/MSI-PROVISIONING.md`). The installer's signature is
   the *only* integrity boundary on that payload — an attacker who can rewrite
   an unsigned MSI can point every machine built from it at a control server of
   their choosing. Until signing lands, a provisioned MSI must be treated as a
   credential and distributed over a trusted channel.

## Provider: SignPath Foundation does not fit

`ARCHITECTURE.md` previously named the SignPath Foundation OSS certificate.
Having checked their terms, it does not apply to this project:

- It requires a **publicly available repository** under an **OSI-approved open
  source license**, with no proprietary components. This repository is private
  and the product is commercial.
- The publisher shown in SmartScreen would be **"SignPath Foundation"**, not
  the vendor. For a product an MSP sells to clients, the publisher name is
  most of the point.

That line in the roadmap is a plan that would have failed at application time,
so it has been corrected rather than left to be discovered later.

## Provider: Azure Artifact Signing (formerly Trusted Signing)

The fit for a commercial, closed-source Windows product:

- Microsoft-managed CA; keys held in **FIPS 140-2 Level 3 HSMs**. Nothing to
  buy, store, or lose — which matters because since June 2023 code signing
  private keys must live on certified hardware, so the old "put a .pfx in a CI
  secret" approach is no longer available to anyone.
- Signs from CI directly; integrates with GitHub Actions.
- Pay-as-you-go, an order of magnitude below a traditional EV certificate.

**Eligibility, and the part worth checking first:** it is offered to
organizations in the **US and Canada** with a **verifiable three-year
history**. The geography is fine here; the three-year org history is the
question to answer before anything else, and answering it takes one attempt at
the identity-validation step in the Azure portal.

Note the service was **renamed from Trusted Signing to Artifact Signing**, so
older guides and the GitHub Action may still carry the previous name. Check
current Microsoft documentation for the action reference rather than copying a
blog post.

### Alternative if the three-year check fails

A conventional OV/EV certificate from a commercial CA, with the key on a
hardware token or a cloud HSM. More expensive and more to administer, but no
organization-age requirement beyond the CA's own vetting.

## The split

**Requires a person with the company identity — cannot be automated:**

1. Choose the provider and complete **organization identity validation**.
2. Create the signing account / obtain the certificate.
3. Add the CI secrets (names below).

**Already handled in CI:**

- Smoke **S8** reads `Get-AuthenticodeSignature` on both shipped binaries and
  reports the result on every build.
- While signing is unconfigured it emits a **warning**, so an unsigned build
  does not red the pipeline for a known gap.
- The moment `SIGNING_CLIENT_ID` exists as a repository secret,
  `SIGNING_ENABLED` flips to true on its own and an invalid or missing
  signature becomes a **hard failure** — no workflow edit at the moment it
  starts mattering. A release that silently *stops* being signed is worse than
  one that was never signed, because the trust has already been claimed.

### Secrets to add

| Secret | Purpose |
|---|---|
| `SIGNING_CLIENT_ID` | Service principal / signing account identity. Presence of this one is what flips the checks to blocking. |
| `SIGNING_CLIENT_SECRET` | Its credential. |
| `SIGNING_TENANT_ID` | Directory tenant. |
| `SIGNING_ACCOUNT_NAME` | Signing account. |
| `SIGNING_CERT_PROFILE` | Certificate profile to sign with. |

### Remaining wiring

The signing **step** itself is the last piece and is deliberately not guessed
at here — the action's name and inputs moved with the rebrand, and a workflow
that references a stale action fails at release time. Once the provider is
chosen and the account exists, adding the step is a small change against the
provider's current documentation, and S8 already proves whether it worked.

Signing artifacts to cover, in order: `NimbusBackupSVC.exe` (the service —
highest AV-heuristic exposure), `NimbusBackup.exe`, then `NimbusBackup.msi`.
Sign the binaries *before* the MSI is built, so the MSI's own signature covers
already-signed contents.
