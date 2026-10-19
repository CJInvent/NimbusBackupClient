package api

import (
	"os"
	"path/filepath"
)

// DataDir is the service's data directory: C:\ProgramData\NimbusBackup on
// Windows, ~/.proxmox-backup-guardian elsewhere. It only names the directory;
// the service creates it (getConfigDir).
//
// It lives here, not in the service, because the console needs one file in it
// (the token, TokenPath) and the console's module cannot import the service's
// code (docs/V4-CONSOLE.md §2 rule 1). One definition, so the two programs
// cannot resolve different paths.
func DataDir() (string, error) {
	if programData := os.Getenv("ProgramData"); programData != "" {
		return filepath.Join(programData, "NimbusBackup"), nil
	}
	if systemDrive := os.Getenv("SystemDrive"); systemDrive != "" {
		// ProgramData unset (it has been seen in service contexts): the
		// service's files must still be where the console looks for them.
		return filepath.Join(systemDrive, "ProgramData", "NimbusBackup"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".proxmox-backup-guardian"), nil
}

// TokenPath is the local API's shared token file (H-01): written by the
// service, read by the console. V4-LOCAL-API P1 replaces it with the named
// pipe's caller identity and deletes it.
func TokenPath() string {
	dir, err := DataDir()
	if err != nil || dir == "" {
		return "nimbus-api-token"
	}
	return filepath.Join(dir, "api-token")
}
