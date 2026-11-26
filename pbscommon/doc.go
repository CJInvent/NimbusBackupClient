// Package pbscommon is the Proxmox Backup Server protocol client: sessions,
// fixed and dynamic indexes, chunks, blobs, manifests, the pxar writer and the
// backup-key crypto.
//
// IT HAS CONSUMERS IN ANOTHER REPOSITORY. NimbusControl builds three server
// helpers from this package at the client SHA pinned in
// volumereader/CLIENT_REF:
//
//   - nimbus-volume-reader (volumereader/, with imagebrowse), so the portal
//     and this machine read a backup with the same code; NimbusControl
//     docs/V4-CLIENT-CHANGES-FOR-SERVER-BROWSE.md section 2 lists the symbols
//     it compiles against;
//   - nimbus-bundle-io (bundleio/), the server's own backup;
//   - nimbus-ns-sync (nssync/), which copies an organization's snapshots from
//     one PBS to another exactly as stored, through rawcopy.go
//     (NimbusControl docs/V4-PBS-MIGRATION.md section 3.6).
//
// A change here is a change to the server the next time that pin moves. Two
// rules follow from it:
//
//   - nothing here writes to stdout (the reader's data channel); diagnostics
//     go through DebugLogFn (see debugf);
//   - no import of gui, Wails or anything Windows-only outside a _windows.go
//     file, so a Linux server can build it (client CI builds it for Linux).
package pbscommon
