// Package imagebrowse reads files out of a disk image backup: the partition
// table, NTFS, FAT and exFAT, listing, extraction and the full-tree scan.
//
// IT HAS A SECOND CONSUMER IN ANOTHER REPOSITORY. NimbusControl builds its
// volume reader, nimbus-volume-reader (NimbusControl volumereader/), from this
// package and pbscommon at the client SHA pinned in volumereader/CLIENT_REF.
// A change here is a change to the server the next time that pin moves
// (NimbusControl docs/V4-CLIENT-CHANGES-FOR-SERVER-BROWSE.md section 2 lists
// the symbols it compiles against). Keep it free of gui, Wails and
// Windows-only imports so a Linux server can build it.
package imagebrowse
