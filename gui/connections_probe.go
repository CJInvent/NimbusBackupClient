//go:build service
// +build service

package main

import (
	"sync"
	"time"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
	"pbscommon"
)

// The connection picture the status panel renders, probed on a cadence rather
// than on demand.
//
// WHY CACHED: /connections is on a poll loop in the front end. Probing every
// PBS destination per request would let a console left open on a technician's
// desk generate steady traffic against every datastore an org owns, and would
// couple the panel's latency to the slowest server. So the HTTP handler always
// answers from memory, and a background sweep refreshes it.
//
// WHY IT NEVER BLOCKS: a destination that is down costs the prober a TCP
// timeout. If that ran inside the handler, one dead server would make the
// whole panel hang — which is precisely the panel that exists to TELL you a
// server is down.
//
// The freshness of each answer travels with it (`CheckedAt`), so the panel can
// say "checked two minutes ago" instead of implying it is live.

const (
	// connProbeInterval is how often every destination is re-probed.
	//
	// Long enough that ten agents polling do not look like a health-check
	// storm to a PBS server; short enough that a technician watching the
	// panel while they fix a firewall rule sees it go green without
	// restarting anything.
	connProbeInterval = 60 * time.Second

	// A per-probe timeout is NOT set here: PBSClient builds its own dialer
	// and HTTP clients with 5-10s timeouts internally (pbscommon/pbsapi.go),
	// and adding a second knob that could disagree with those would be a
	// second source of truth for the same fact. The sweep is serial and
	// bounded by those, which is why it runs in a goroutine and the handler
	// never waits on it.
)

var (
	connMu     sync.RWMutex
	connCached []api.PBSConnection
)

// startConnectionProbe runs the sweep until the process exits.
func (a *App) startConnectionProbe() {
	go func() {
		// Probe immediately so the first panel load is not empty, then on
		// the interval.
		a.probeConnectionsOnce()
		t := time.NewTicker(connProbeInterval)
		defer t.Stop()
		for range t.C {
			a.probeConnectionsOnce()
		}
	}()
}

// probeConnectionsOnce checks every configured destination and replaces the
// cache with the result.
func (a *App) probeConnectionsOnce() {
	cfg := a.config
	if cfg == nil {
		return
	}

	servers := cfg.PBSServers
	out := make([]api.PBSConnection, 0, len(servers)+1)

	if len(servers) == 0 {
		// Legacy single-server config: the top-level fields ARE the
		// destination, and a panel that showed nothing here would be wrong
		// about a machine that is backing up perfectly well.
		eff := cfg.EffectivePBS()
		if eff.Validate() == nil {
			out = append(out, a.probeOne("default", "Default", eff.BaseURL, eff.Datastore, true, eff))
		}
		a.storeConnections(out)
		return
	}

	for id, s := range servers {
		if s == nil {
			continue
		}
		probeCfg := &Config{
			BaseURL: s.BaseURL, AuthID: s.AuthID, Secret: s.Secret,
			Datastore: s.Datastore, Namespace: s.Namespace,
			CertFingerprint: s.CertFingerprint,
		}
		out = append(out, a.probeOne(id, s.Name, s.BaseURL, s.Datastore,
			id == cfg.DefaultPBSID, probeCfg))
	}
	a.storeConnections(out)
}

// probeOne checks a single destination. Reachable stays nil when the check
// could not be RUN, which is a different fact from the server saying no.
func (a *App) probeOne(id, name, host, datastore string, isDefault bool, cfg *Config) api.PBSConnection {
	conn := api.PBSConnection{
		ID: id, Name: name, Host: host, Datastore: datastore,
		IsDefault: isDefault, CheckedAt: time.Now(),
	}

	if err := cfg.Validate(); err != nil {
		// Half-configured: not unreachable, unknowable. Saying "offline"
		// would send someone to check a firewall for a config problem.
		return conn
	}

	client := &pbscommon.PBSClient{
		BaseURL: cfg.BaseURL, AuthID: cfg.AuthID, Secret: cfg.Secret,
		Datastore: cfg.Datastore, Namespace: cfg.Namespace,
		CertFingerPrint: cfg.CertFingerprint,
	}
	reachable, err := client.CheckConnectivity()
	if err != nil {
		writeDebugLog("[connections] probe of " + id + " could not run: " + err.Error())
		return conn
	}
	conn.Reachable = &reachable
	return conn
}

func (a *App) storeConnections(c []api.PBSConnection) {
	connMu.Lock()
	connCached = c
	connMu.Unlock()
}

// connectionsSnapshot is what the local API serves. Cheap by construction: a
// copy of the cache plus the control-plane state, which the agent already
// holds in memory.
func (a *App) connectionsSnapshot() api.ConnectionsResponse {
	connMu.RLock()
	pbs := make([]api.PBSConnection, len(connCached))
	copy(pbs, connCached)
	connMu.RUnlock()

	out := api.ConnectionsResponse{PBS: pbs, ServerTime: time.Now()}

	if a.config != nil && a.config.ControlServerURL != "" {
		out.ControlPlane.Configured = true
		out.ControlPlane.ServerHost = a.config.ControlServerURL
	}
	if ag := cpAgent; ag != nil {
		st := ag.Status()
		out.ControlPlane.Connected = st.Connected
		out.ControlPlane.LastSuccess = st.LastSuccess
		out.ControlPlane.LastError = st.LastError
	}
	return out
}
