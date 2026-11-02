package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// retiredAPITokenFile is the shared local-API token (H-01) that versions
// before V4-LOCAL-API P1 wrote into the data directory. The pipe identifies
// its callers from Windows, and nothing reads the file now.
const retiredAPITokenFile = "api-token"

// removeRetiredAPIToken deletes the token file an older version left behind.
// It authorized nothing after the upgrade, but a credential-shaped file that
// every interactive user could read is not something to leave lying about.
func removeRetiredAPIToken() {
	dir, err := dataDir()
	if err != nil {
		return
	}
	path := filepath.Join(dir, retiredAPITokenFile)
	switch err := os.Remove(path); {
	case err == nil:
		writeInfoLog("Removed the retired local API token file " + path)
	case !errors.Is(err, fs.ErrNotExist):
		writeWarnLog(fmt.Sprintf("Could not remove the retired local API token file %s: %v", path, err))
	}
}
