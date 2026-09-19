package main

import (
	"controlplane"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func (a *App) storageController() (*controlplane.StorageState, error) {
	if !a.isServiceProcess {
		return nil, errors.New("storage identity is owned by the service")
	}
	a.storageMu.Lock()
	defer a.storageMu.Unlock()
	if a.storageState != nil {
		return a.storageState, nil
	}
	dir, err := getConfigDir()
	if err != nil {
		return nil, err
	}
	s, err := controlplane.OpenStorageState(filepath.Join(dir, "storage-identity.json"), restrictToServiceOnly)
	if err == nil {
		a.storageState = s
	}
	return s, err
}
func (a *App) refreshStorageIdentity() controlplane.StorageStatus {
	a.storageOpMu.Lock()
	defer a.storageOpMu.Unlock()
	return a.refreshStorageIdentityLocked()
}
func (a *App) refreshStorageIdentityLocked() controlplane.StorageStatus {
	s, err := a.storageController()
	if err != nil {
		return a.rememberStorageFailure(controlplane.StorageStatus{}, "storage state unavailable: "+err.Error())
	}
	before := s.Snapshot()
	devices, discoveryErr := discoverStorageDevices()
	a.storageMu.Lock()
	pending := a.storageFailure
	a.storageMu.Unlock()
	if pending != "" {
		discoveryErr = errors.New(pending)
	}
	if err = s.Observe(devices, discoveryErr); err != nil {
		return a.rememberStorageFailure(before, "storage state persistence failed: "+err.Error())
	}
	a.storageMu.Lock()
	a.storageFailure = "" // The fault is now durably latched by Observe.
	a.storageMu.Unlock()
	after := s.Snapshot()
	if after.Error != before.Error && after.Error != "" {
		writeErrorLog("[StorageIdentity] " + after.Error)
	}
	return storageWireStatus(after)
}
func (a *App) applyStorageApproval(approval *controlplane.StorageApproval) {
	a.storageOpMu.Lock()
	defer a.storageOpMu.Unlock()
	if approval == nil {
		return
	}
	s, err := a.storageController()
	if err != nil {
		writeErrorLog("[StorageIdentity] " + err.Error())
		return
	}
	if approval.Revision <= s.Snapshot().Revision {
		return
	}
	before := a.refreshStorageIdentityLocked()
	if err = s.Approve(*approval); err != nil {
		writeErrorLog("[StorageIdentity] dashboard approval refused: " + err.Error())
		return
	}
	logStorageApproval("dashboard", before, s.Snapshot())
}

// StorageIdentityStatusMap is the service's status for both local UI and API.
func (a *App) StorageIdentityStatusMap() map[string]interface{} {
	s, err := a.storageController()
	st := controlplane.StorageStatus{}
	if err != nil {
		st.Error = err.Error()
	} else {
		st = s.Snapshot()
	}
	a.storageMu.Lock()
	if a.storageFailure != "" {
		st.Error = a.storageFailure
	}
	a.storageMu.Unlock()
	st = storageWireStatus(st)
	b, _ := json.Marshal(st)
	out := map[string]interface{}{}
	_ = json.Unmarshal(b, &out)
	out["joined"] = a.config != nil && a.config.ControlServerURL != ""
	if a.config != nil {
		out["dashboard_url"] = a.config.ControlServerURL
		out["agent_id"] = a.config.ControlAgentID
	}
	return out
}
func (a *App) ApproveStorageIdentityFromMap(input map[string]interface{}) error {
	a.storageOpMu.Lock()
	defer a.storageOpMu.Unlock()
	if a.config != nil && a.config.ControlServerURL != "" {
		return errors.New("joined agents must resolve storage identity through the dashboard")
	}
	b, err := json.Marshal(input)
	if err != nil {
		return err
	}
	var approval controlplane.StorageApproval
	if err = json.Unmarshal(b, &approval); err != nil {
		return err
	}
	st := a.refreshStorageIdentityLocked()
	s, err := a.storageController()
	if err != nil {
		return err
	}
	if err = s.Approve(approval); err != nil {
		return err
	}
	logStorageApproval("local GUI", st, s.Snapshot())
	return nil
}

// GetStorageIdentityStatus delegates to the same authenticated local API as configuration.
// An unreachable service is an error, never permission to write locally.
func (a *App) GetStorageIdentityStatus() (map[string]interface{}, error) {
	if !a.isServiceProcess {
		if a.apiClient == nil {
			return nil, errors.New("[NB-2013] Storage identity service unavailable")
		}
		status, err := a.apiClient.GetStorageIdentity()
		if err == nil {
			a.updateStorageTray(status)
		}
		return status, err
	}
	a.refreshStorageIdentity()
	status := a.StorageIdentityStatusMap()
	a.updateStorageTray(status)
	return status, nil
}
func (a *App) ApproveStorageIdentity(input map[string]interface{}) error {
	if !a.isServiceProcess {
		if a.apiClient == nil {
			return errors.New("[NB-2013] Storage identity service unavailable")
		}
		return a.apiClient.ApproveStorageIdentity(input)
	}
	return a.ApproveStorageIdentityFromMap(input)
}

func logStorageApproval(authority string, before, after controlplane.StorageStatus) {
	evidence, _ := json.Marshal(map[string]interface{}{"authority": authority, "old_revision": before.Revision, "revision": after.Revision, "observation": after.Observation, "old_bindings": before.Bindings, "new_bindings": after.Bindings, "outcome": "applied"})
	writeBackupLog("[StorageIdentity] approval " + string(evidence))
}

// Even a failed state read must produce valid telemetry, otherwise check-in
// rejection hides the fault from every server panel. Retain the fault in memory
// until the next successful Observe durably latches it for explicit approval.
func (a *App) rememberStorageFailure(st controlplane.StorageStatus, message string) controlplane.StorageStatus {
	a.storageMu.Lock()
	if a.storageFailure == "" {
		a.storageFailure = message
	}
	st.Error = a.storageFailure
	a.storageMu.Unlock()
	return storageWireStatus(st)
}
func storageWireStatus(st controlplane.StorageStatus) controlplane.StorageStatus {
	if st.Observation == "" {
		st.Observation = controlplane.StorageObservation(st.Devices)
	}
	st.Error = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, st.Error)
	if len(st.Error) > 512 {
		st.Error = st.Error[:512]
		for !utf8.ValidString(st.Error) {
			st.Error = st.Error[:len(st.Error)-1]
		}
	}
	return st
}
