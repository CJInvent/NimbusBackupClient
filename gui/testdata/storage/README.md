# Captured storage descriptors (F-39 / F-41)

Raw `IOCTL_STORAGE_QUERY_PROPERTY` output from the test VM, captured with
NimbusControl's `docs/reverify-2026-09-27/C0-Capture.ps1 -Part Storage -Label <label>`
(ledger prerequisite C0). For each capture commit:

- `<label>.json`: the script's `disks.json`, unedited;
- `<label>.want.json`: `{"<disk number>": "weak" | "strong"}` for the disks
  whose configuration is known (for example the test disk with no `serial=`,
  with `serial=`, with `wwn=`).

`TestStorageIdentityCapturedDescriptors` reads every pair. None are committed
yet (2026-09-27): until they are, the pin is the two device ids the pre-F-39
client computed on the VM's test disk at scsi1 and scsi2
(`docs/V4-BETA-EVIDENCE-2026-09-23.md`, 2026-09-24), which the parser must
reproduce exactly.
