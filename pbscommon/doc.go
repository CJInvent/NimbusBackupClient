// Package pbscommon is the Proxmox Backup Server protocol client: sessions,
// fixed and dynamic indexes, chunks, blobs, manifests, the pxar writer and the
// backup-key crypto.
//
// IT HAS A SECOND CONSUMER IN ANOTHER REPOSITORY. NimbusControl builds its
// volume reader, nimbus-volume-reader (NimbusControl volumereader/), from this
// package and imagebrowse at the client SHA pinned in volumereader/CLIENT_REF,
// so the portal and this machine read a backup with the same code. A change
// here is a change to the server the next time that pin moves:
// NimbusControl docs/V4-CLIENT-CHANGES-FOR-SERVER-BROWSE.md section 2 lists
// the symbols it compiles against. Two rules follow from it:
//
//   - nothing here writes to stdout (the reader's data channel); diagnostics
//     go through DebugLogFn (see debugf);
//   - no import of gui, Wails or anything Windows-only outside a _windows.go
//     file, so a Linux server can build it (client CI builds it for Linux).
package pbscommon
