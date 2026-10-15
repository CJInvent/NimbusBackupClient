package main

import (
	"errors"
	"fmt"
	"sync"

	"controlplane"
)

// Data path grants, the half both builds compile: where a grant ARRIVES (a
// run_backup command, a handshake) and is held. Where it is OPENED and used
// is service-only (datapath_service.go): only the service runs an engine.

// ---------------------------------------------------- grant handoff to a run
//
// A grant arrives where the run is DECIDED (a run_backup command, the GUI's
// handshake, a local job's handshake) and is used where its options are BUILT
// (the pipeline's resolve). The handoff is keyed exactly like the run
// reporter's (registerRunReporter / takeRunReporter): by the job's backup id,
// or one pending slot for a job with none.

var (
	runGrantsMu        sync.Mutex
	runGrantsByRequest = map[string]*controlplane.Grant{}
)

// stashRunGrant holds the grant that came with a request until the run that
// answers it is announced.
func stashRunGrant(requestID string, g *controlplane.Grant) {
	if requestID == "" || g == nil {
		return
	}
	runGrantsMu.Lock()
	defer runGrantsMu.Unlock()
	runGrantsByRequest[requestID] = g
}

// grantFromPayload reads a run_backup command's `grant`.
func grantFromPayload(p map[string]interface{}) *controlplane.Grant {
	raw, ok := p["grant"].(map[string]interface{})
	if !ok {
		return nil
	}
	s := func(k string) string { v, _ := raw[k].(string); return v }
	g := &controlplane.Grant{AuthID: s("auth_id"), SecretSealedB64: s("secret_sealed"),
		SealedToKeyID: s("sealed_to_key_id"), BaseURL: s("base_url"), Fingerprint: s("fingerprint"),
		Datastore: s("datastore"), Namespace: s("namespace"), Kind: s("kind"), RedeemBy: s("redeem_by")}
	if ids, ok := raw["backup_ids"].([]interface{}); ok {
		for _, b := range ids {
			if m, ok := b.(map[string]interface{}); ok {
				src, _ := m["source"].(string)
				id, _ := m["id"].(string)
				g.BackupIDs = append(g.BackupIDs, controlplane.GrantBackupID{Source: src, ID: id})
			}
		}
	}
	if fb, ok := raw["fallbacks"].([]interface{}); ok {
		for _, f := range fb {
			if m, ok := f.(map[string]interface{}); ok {
				u, _ := m["base_url"].(string)
				fp, _ := m["fingerprint"].(string)
				g.Fallbacks = append(g.Fallbacks, controlplane.GrantEndpoint{BaseURL: u, Fingerprint: fp})
			}
		}
	}
	return g
}

// requestLocalRun is the handshake for a backup this machine decided to run
// itself (V4-RUN-AUDIT 1.3a): the server answers a request id -- and, in
// proxy mode, the grant, which is held for the run -- or refuses. sources
// are a directory backup's directories, so the grant names an id for each.
func requestLocalRun(jobName string, sources []string) (string, error) {
	cpMu.Lock()
	c := cpClient
	cpMu.Unlock()
	if c == nil {
		return "", errors.New("the control server is not connected, and this machine runs no backup without its answer")
	}
	resp, err := c.RequestBackup(controlplane.BackupRequest{JobName: jobName, Managed: false, Sources: sources})
	if err != nil {
		return "", fmt.Errorf("the control server did not allow this backup: %w", err)
	}
	stashRunGrant(resp.RequestID, resp.Grant)
	return resp.RequestID, nil
}

// directorySources is what the handshake and the inventory name for a job:
// a directory job's directories, exactly as the job holds them; nothing for
// an image job.
func directorySources(backupType string, dirs []string) []string {
	if backupType != "directory" || len(dirs) == 0 {
		return nil
	}
	return dirs
}
