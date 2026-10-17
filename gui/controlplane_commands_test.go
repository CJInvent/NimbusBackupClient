package main

import (
	"controlplane"
	"testing"
)

// NimbusControl docs/V4-CLIENT-CHANGES-FOR-SERVER-BROWSE.md §1: the server
// reads backups itself and never asks a machine to browse one. A command
// from the old vocabulary is refused as unknown, so the vocabulary cannot come
// back without someone deleting this test.
func TestTheDispatcherDoesNotBrowseImagesForThePortal(t *testing.T) {
	a := &App{config: &Config{}}
	for _, name := range []string{"image_partitions", "image_scan", "image_dir", "image_extract"} {
		res := a.cpHandleCommand(controlplane.Command{ID: 1, Command: name,
			Payload: map[string]interface{}{"disk": "drive-scsi0.img.fidx", "part": float64(1)}})
		if res.OK || res.Result["error"] != "unsupported command: "+name {
			t.Errorf("%s: %+v, want refused as an unsupported command", name, res)
		}
	}
}
