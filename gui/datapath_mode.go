package main

import (
	"errors"
	"fmt"
	"sync/atomic"

	"controlplane"
)

// The data path, client half (NimbusControl docs/V4-DATAPATH-PROXY.md §13).
//
// When the control server runs its data path, this machine reaches PBS ONLY
// through the server's instances, and only with a grant minted for one
// operation. It holds no PBS credential and never learns where its backups
// are (D2): a machine is untrusted hardware, and what it does not hold it
// cannot leak. Standalone installs never get here -- the mode comes only from
// a control server's check-in.

// errPBSSettingsServerManaged is the local API's answer to any PBS settings
// write in proxy mode. The GUI hides those controls; the service refuses them,
// because a machine that could be handed a PBS server again would no longer be
// one that cannot leak where its backups are.
var errPBSSettingsServerManaged = errors.New("this machine's backups go through its control server's data path; it has no PBS settings to change")

// dpProxy mirrors config.DatapathMode for the hot paths that cannot take the
// config lock (the scheduler tick, the pipeline's resolve).
var dpProxy atomic.Bool

// datapathProxy reports whether this machine is in proxy mode.
func datapathProxy() bool { return dpProxy.Load() }

// initDatapathMode restores the last mode the server stated, so a restart
// behaves correctly before its first check-in.
func initDatapathMode(cfg *Config) {
	dpProxy.Store(cfg != nil && cfg.DatapathMode == controlplane.DatapathModeProxy)
}

// applyDatapathFromCheckin is the OnDatapath hook: every check-in, before
// commands. nil (an older server) is direct.
func (a *App) applyDatapathFromCheckin(d *controlplane.DatapathCheckin) {
	cfg := a.config
	if cfg == nil {
		return
	}
	mode := controlplane.DatapathModeDirect
	if d.Proxy() {
		mode = controlplane.DatapathModeProxy
	}
	changed := cfg.DatapathMode != mode
	wiped := false
	if mode == controlplane.DatapathModeProxy {
		wiped = forgetPBSCredential(cfg)
	}
	if changed || wiped {
		cfg.DatapathMode = mode
		if err := cfg.Save(); err != nil {
			writeErrorLog(fmt.Sprintf("[DataPath] ERROR: could not record the data path mode: %v", err))
		}
	}
	dpProxy.Store(mode == controlplane.DatapathModeProxy)
	if changed {
		if mode == controlplane.DatapathModeProxy {
			writeInfoLog("[DataPath] the control server routes this machine's backups and restores through its data path; this machine holds no PBS credential")
		} else {
			writeInfoLog("[DataPath] the control server no longer uses its data path for this machine")
		}
	}
	if wiped {
		writeInfoLog("[DataPath] removed the PBS settings this machine held: in proxy mode it never knows where its backups are")
	}
	if mode != controlplane.DatapathModeProxy {
		return
	}

	// The machine certificate every data-path connection presents.
	if needed, why := datapathCertNeeded(cfg, d.CertRenew); needed {
		cpMu.Lock()
		client := cpClient
		cpMu.Unlock()
		if client != nil {
			if err := renewDatapathIdentity(client, cfg.ControlAgentID, why); err != nil {
				writeWarnLog(fmt.Sprintf("[DataPath] WARNING: no machine certificate yet: %v", err))
			}
		}
	}
}

// forgetPBSCredential erases every PBS setting an enrolled machine may hold
// -- the server-provisioned target and secret, and any hand-entered server --
// reporting whether anything was there. In proxy mode none of it may remain:
// "enrolled clients lose direct PBS completely" (CJ, 2026-10-12).
func forgetPBSCredential(cfg *Config) bool {
	had := cfg.BaseURL != "" || cfg.AuthID != "" || cfg.Secret != "" || cfg.Datastore != "" ||
		cfg.Namespace != "" || cfg.CertFingerprint != "" || cfg.PBSCredentialFor != nil ||
		cfg.PBSRefusedAt != 0 || len(cfg.PBSServers) > 0 || cfg.DefaultPBSID != ""
	cfg.BaseURL, cfg.AuthID, cfg.Secret = "", "", ""
	cfg.Datastore, cfg.Namespace, cfg.CertFingerprint = "", "", ""
	cfg.PBSCredentialFor, cfg.PBSRefusedAt = nil, 0
	cfg.PBSServers, cfg.DefaultPBSID = nil, ""
	return had
}
