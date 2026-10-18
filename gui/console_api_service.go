//go:build service
// +build service

package main

// The service's answers to the routes the console still lacked
// (docs/V4-CONSOLE.md §4.1, phase C1): PBS servers read and probed, the
// machine's disks and Exchange, and the download space check. Each one is
// something the console did itself, in the user's session, with its own
// decrypted copy of config.json; after V4-CONSOLE C2 it asks here instead.
//
// `service`-tagged: these are entry points of the service, not webview
// bindings. Every exported App method in an untagged file is bound into the
// console by Wails (V4-CONSOLE §1.2), and these must not be.

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
	"pbscommon"
	"security"
)

// The service build fails here, not at a type assertion that quietly answers
// 501 at runtime, if a method is renamed on either side.
var (
	_ api.PBSHandler     = (*App)(nil)
	_ api.MachineHandler = (*App)(nil)
)

// PBSServersForAPI is GET /pbs/servers: every configured server, sanitized by
// type (api.PBSServerInfo has no secret field), sorted by id so the list does
// not reorder itself between two reads of the same map.
func (a *App) PBSServersForAPI() api.PBSServersResponse {
	if datapathProxy() {
		return api.PBSServersResponse{Servers: []api.PBSServerInfo{}, DatapathProxy: true}
	}
	cfg := a.config
	if cfg == nil {
		return api.PBSServersResponse{Servers: []api.PBSServerInfo{}}
	}
	out := api.PBSServersResponse{
		Servers:   make([]api.PBSServerInfo, 0, len(cfg.PBSServers)),
		DefaultID: cfg.DefaultPBSID,
	}
	for _, s := range cfg.ListPBSServers() {
		out.Servers = append(out.Servers, s.apiInfo())
	}
	sort.Slice(out.Servers, func(i, j int) bool { return out.Servers[i].ID < out.Servers[j].ID })
	return out
}

// apiInfo is this server as the console may see it.
func (pbs *PBSServer) apiInfo() api.PBSServerInfo {
	return api.PBSServerInfo{
		ID:              pbs.ID,
		Name:            pbs.Name,
		BaseURL:         pbs.BaseURL,
		CertFingerprint: pbs.CertFingerprint,
		AuthID:          pbs.AuthID,
		Datastore:       pbs.Datastore,
		Namespace:       pbs.Namespace,
		Description:     pbs.Description,
		SecretSet:       pbs.Secret != "",
	}
}

// TestPBSServerForAPI is POST /pbs/test: the configured server's connection
// test, run with the stored token, which stays in this process. A test that
// ran and failed is an outcome (OK false), not an error; the errors are for
// the request itself (no such server, proxy mode).
func (a *App) TestPBSServerForAPI(id string) (api.PBSTestResponse, error) {
	if datapathProxy() {
		return api.PBSTestResponse{}, api.Refuse(http.StatusConflict, errPBSSettingsServerManaged)
	}
	cfg := a.config
	var pbs *PBSServer
	if cfg != nil {
		pbs = cfg.PBSServers[id]
	}
	if pbs == nil {
		return api.PBSTestResponse{}, api.Refuse(http.StatusNotFound, fmt.Errorf("no PBS server with id %q is configured", id))
	}

	writeInfoLog(fmt.Sprintf("TestPBSServerForAPI(%s) called", id))
	target := pbs.ToConfig()
	err := target.Validate()
	if err == nil {
		err = target.connectionTest()
	}
	if err != nil {
		return api.PBSTestResponse{
			Error:         err.Error(),
			CertUntrusted: target.CertFingerprint == "" && isCertVerifyFailure(err),
		}, nil
	}
	return api.PBSTestResponse{OK: true}, nil
}

// isCertVerifyFailure reports whether err is the server's certificate failing
// verification: what pinning its fingerprint resolves, since a pinned
// connection skips CA verification for exactly that certificate.
func isCertVerifyFailure(err error) bool {
	var cv *tls.CertificateVerificationError
	return errors.As(err, &cv)
}

// DiscoverPBSFingerprintForAPI is POST /pbs/discover: a TLS handshake with the
// server at url and the SHA-256 of the certificate it presents. No token is
// sent; nothing but the handshake happens. Refused in proxy mode, where there
// is nothing to pin.
func (a *App) DiscoverPBSFingerprintForAPI(url string) (string, error) {
	if datapathProxy() {
		return "", api.Refuse(http.StatusConflict, errPBSSettingsServerManaged)
	}
	if err := security.ValidateURL(url); err != nil {
		return "", api.Refuse(http.StatusBadRequest, fmt.Errorf("%s :: %v", errInvalidURL, err))
	}
	writeInfoLog(fmt.Sprintf("DiscoverPBSFingerprintForAPI(%s) called", security.SanitizeURL(url)))
	fp, err := pbscommon.FetchServerFingerprint(url)
	if err != nil {
		writeWarnLog(fmt.Sprintf("DiscoverPBSFingerprintForAPI failed: %v", err))
		return "", api.Refuse(http.StatusBadGateway, err)
	}
	return fp, nil
}

// PhysicalDisksForAPI is GET /disks: the enumeration disk_inventory.go reports
// to the control server, read as SYSTEM rather than from the user's session.
func (a *App) PhysicalDisksForAPI() ([]api.PhysicalDisk, error) {
	return ListPhysicalDisks()
}

// ExchangeForAPI is GET /exchange. The log-mode query runs the Exchange
// Management Shell and takes seconds, so it runs only when asked for, and only
// where Exchange is installed.
func (a *App) ExchangeForAPI(withLogMode bool) api.ExchangeResponse {
	out := api.ExchangeResponse{Status: a.GetExchangeStatus()}
	if withLogMode && out.Status.Installed {
		lm := a.QueryExchangeLogMode()
		out.LogMode = &lm
	}
	return out
}

// checkDownloadSpace is the restore op "space": the console's warning dialog
// before a download, answered for the drive the service will write to. The
// check that blocks a download that will not fit is still the one inside
// download_service.go; this one is the pre-flight.
func checkDownloadSpace(p SpaceParams) (SpaceCheck, error) {
	if p.DestPath == "" {
		return SpaceCheck{}, errors.New(errDestPathRequired)
	}
	if err := security.ValidatePath(p.DestPath); err != nil {
		return SpaceCheck{}, fmt.Errorf("invalid destination path: %w", err)
	}
	needed := uint64(0)
	if p.NeededBytes > 0 {
		needed = uint64(p.NeededBytes)
	}
	return evaluateSpace(p.DestPath, needed)
}
