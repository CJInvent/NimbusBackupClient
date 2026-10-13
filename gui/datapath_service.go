//go:build service
// +build service

package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"controlplane"
	"pbscommon"
)

// Grants become the PBS target the engines already understand (NimbusControl
// docs/V4-DATAPATH-PROXY.md §13.3): the instance's URL and pin, the grant id
// as the auth-id, the opened secret, the VIRTUAL datastore and no namespace,
// plus the DataPath the PBS client carries (machine certificate, fallbacks).
// Service-only: only the service runs an engine.

// runGrantsByBackup / pendingRunGrant hold a grant bound to an announced run,
// keyed exactly like the run reporter (registerRunReporter / takeRunReporter).
var (
	runGrantsByBackup = map[string]*controlplane.Grant{}
	pendingRunGrant   *controlplane.Grant
)

// errNoGrant: proxy mode and nothing authorized this operation.
var errNoGrant = errors.New("this machine backs up only through the control server's data path, and no grant came with this run")

// grantTarget opens a grant into a PBS target.
func grantTarget(g *controlplane.Grant) (*Config, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	sealed, err := base64.StdEncoding.DecodeString(g.SecretSealedB64)
	if err != nil {
		return nil, fmt.Errorf("the grant's secret is not valid base64: %w", err)
	}
	secret, err := openSealedToAgent(sealed, g.SealedToKeyID)
	if err != nil {
		return nil, fmt.Errorf("the grant could not be opened with this machine's key: %w", err)
	}
	if len(secret) == 0 {
		return nil, errors.New("the grant opened to nothing")
	}
	cert, err := loadDatapathCert()
	if err != nil {
		return nil, fmt.Errorf("no machine certificate for the data path: %w", err)
	}
	eps := g.Endpoints()
	dp := &pbscommon.DataPath{Cert: cert, Endpoints: make([]pbscommon.Endpoint, 0, len(eps))}
	for _, e := range eps {
		dp.Endpoints = append(dp.Endpoints, pbscommon.Endpoint{BaseURL: e.BaseURL, Fingerprint: e.Fingerprint})
	}
	return &Config{BaseURL: g.BaseURL, CertFingerprint: g.Fingerprint, AuthID: g.AuthID, Secret: string(secret),
		Datastore: g.Datastore, Namespace: g.Namespace, DataPath: dp}, nil
}

// bindRunGrant moves a request's grant to the run being announced for it.
func bindRunGrant(requestID, backupID string) {
	runGrantsMu.Lock()
	defer runGrantsMu.Unlock()
	g, ok := runGrantsByRequest[requestID]
	if !ok {
		return
	}
	delete(runGrantsByRequest, requestID)
	if backupID != "" {
		runGrantsByBackup[backupID] = g
		return
	}
	pendingRunGrant = g
}

// takeRunGrant is the grant for the run being built, or nil.
func takeRunGrant(backupID string) *controlplane.Grant {
	runGrantsMu.Lock()
	defer runGrantsMu.Unlock()
	if g, ok := runGrantsByBackup[backupID]; ok && backupID != "" {
		delete(runGrantsByBackup, backupID)
		return g
	}
	if pendingRunGrant != nil {
		g := pendingRunGrant
		pendingRunGrant = nil
		return g
	}
	return nil
}

// ------------------------------------------------------ the browse grant
//
// Restore, browse, search and image browse share ONE browse grant (no
// snapshot named): the server closes it after 15 idle minutes, so it is
// reused only while it was used in the last 10 and asked for afresh after
// that.

const browseGrantReuse = 10 * time.Minute

var (
	browseMu       sync.Mutex
	browseTarget   *Config
	browseLastUsed time.Time
)

// browseGrantRequester is the one call a browse target needs, so a test can
// stand in for the control server.
type browseGrantRequester interface {
	RequestRestore(req controlplane.RestoreRequest) (*controlplane.Grant, error)
}

// datapathBrowseTarget returns the PBS target for reading this machine's own
// snapshots through the data path.
func datapathBrowseTarget(r browseGrantRequester) (*Config, error) {
	browseMu.Lock()
	defer browseMu.Unlock()
	if browseTarget != nil && time.Since(browseLastUsed) < browseGrantReuse {
		browseLastUsed = time.Now()
		return browseTarget, nil
	}
	if r == nil {
		return nil, errors.New("this machine restores only through the control server, which is not connected")
	}
	uuid, err := controlplane.NewRestoreUUID()
	if err != nil {
		return nil, err
	}
	g, err := r.RequestRestore(controlplane.RestoreRequest{RestoreUUID: uuid})
	if err != nil {
		return nil, fmt.Errorf("the control server did not allow this restore: %w", err)
	}
	t, err := grantTarget(g)
	if err != nil {
		return nil, err
	}
	browseTarget, browseLastUsed = t, time.Now()
	return t, nil
}

// currentControlPlaneClientForGrants is the control-plane client as the one
// interface a browse target needs; a nil interface (not a typed nil) when
// there is none.
func currentControlPlaneClientForGrants() browseGrantRequester {
	cpMu.Lock()
	c := cpClient
	cpMu.Unlock()
	if c == nil {
		return nil
	}
	return c
}
