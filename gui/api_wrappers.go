package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"security"
)

// API wrapper methods for HTTP API compatibility
// These methods convert map[string]interface{} to typed structs

// SetProgressCallbacks registers per-job progress/stats/completion callbacks for
// API mode. Shared file: the SERVICE build needs this too — it used to live in
// the !service main.go only, so the service never implemented the interface the
// API server asserts, the progress map never updated during a run, and the GUI
// showed a frozen "Starting backup..." for the entire backup.
func (a *App) SetProgressCallbacks(jobID string, onProgress func(string, float64, string), onStats func(string, uint64, uint64, uint64, uint64), onComplete func(string, bool, string)) {
	writeDebugLog(fmt.Sprintf("[SetProgressCallbacks] Registered callbacks for jobID: %s", jobID))
	a.callbacksMutex.Lock()
	a.callbacksMap[jobID] = &progressCallbacks{
		onProgress: onProgress,
		onStats:    onStats,
		onComplete: onComplete,
	}
	a.callbacksMutex.Unlock()
}

// notifyProgressCallbacks fans a progress update (percent 0-100) out to all
// registered per-job callbacks. Returns whether any callback was registered.
func (a *App) notifyProgressCallbacks(percent float64, message string) bool {
	a.callbacksMutex.RLock()
	defer a.callbacksMutex.RUnlock()
	for jobID, callbacks := range a.callbacksMap {
		if callbacks.onProgress != nil {
			callbacks.onProgress(jobID, percent, message)
		}
	}
	return len(a.callbacksMap) > 0
}

// notifyStatsCallbacks fans structured stats out to registered callbacks.
func (a *App) notifyStatsCallbacks(bytesDone, bytesTotal, newChunks, reusedChunks uint64) {
	a.callbacksMutex.RLock()
	defer a.callbacksMutex.RUnlock()
	for jobID, callbacks := range a.callbacksMap {
		if callbacks.onStats != nil {
			callbacks.onStats(jobID, bytesDone, bytesTotal, newChunks, reusedChunks)
		}
	}
}

// notifyCompleteCallbacks fans completion out and clears the registry.
func (a *App) notifyCompleteCallbacks(success bool, message string) bool {
	a.callbacksMutex.Lock()
	defer a.callbacksMutex.Unlock()
	had := len(a.callbacksMap) > 0
	for jobID, callbacks := range a.callbacksMap {
		if callbacks.onComplete != nil {
			callbacks.onComplete(jobID, success, message)
		}
		delete(a.callbacksMap, jobID)
	}
	return had
}

// SaveScheduledJobFromMap is an API wrapper that accepts map[string]interface{}
func (a *App) SaveScheduledJobFromMap(jobData map[string]interface{}) error {
	// Convert map to JSON then unmarshal to ScheduledJob
	jsonData, err := json.Marshal(jobData)
	if err != nil {
		return fmt.Errorf("failed to marshal job data: %w", err)
	}

	var job ScheduledJob
	if err := json.Unmarshal(jsonData, &job); err != nil {
		return fmt.Errorf("failed to unmarshal job data: %w", err)
	}

	return a.SaveScheduledJob(job)
}

// UpdateScheduledJobFromMap is an API wrapper that accepts map[string]interface{}
func (a *App) UpdateScheduledJobFromMap(jobData map[string]interface{}) error {
	jsonData, err := json.Marshal(jobData)
	if err != nil {
		return fmt.Errorf("failed to marshal job data: %w", err)
	}

	var job ScheduledJob
	if err := json.Unmarshal(jsonData, &job); err != nil {
		return fmt.Errorf("failed to unmarshal job data: %w", err)
	}

	return a.UpdateScheduledJob(job)
}

// DeleteScheduledJobFromMap is an API wrapper (same signature, just for consistency)
func (a *App) DeleteScheduledJobFromMap(jobID string) error {
	return a.DeleteScheduledJob(jobID)
}

// pinFingerprintLocal writes the pinned certificate fingerprint to the PBS server
// identified by id, in THIS process's config. It is the privileged write: in
// service mode the service (which owns config.json under ProgramData) performs it
// on the GUI's behalf via the local API, because the unprivileged GUI cannot
// overwrite a service-owned config file — TOFU pinning would otherwise never
// persist and the connection test would keep reporting the server offline.
func (a *App) pinFingerprintLocal(id, fingerprint string) error {
	if err := security.ValidateFingerprint(fingerprint); err != nil {
		return fmt.Errorf("empreinte certificat invalide: %w", err)
	}
	pbs, err := a.config.GetPBSServer(id)
	if err != nil {
		return err
	}
	pbs.CertFingerprint = fingerprint
	if err := a.config.UpdatePBSServer(pbs); err != nil {
		writeDebugLog(fmt.Sprintf("pinFingerprintLocal: could not persist fingerprint for %q: %v", id, err))
		return fmt.Errorf("impossible d'enregistrer l'empreinte (config.json non accessible en écriture ?): %w", err)
	}
	// Read the file back so the log states unambiguously whether the fingerprint
	// reached disk — this separates a write/permission failure from a TLS-apply bug
	// when diagnosing why a pinned certificate still tests offline.
	normalize := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, ":", "")) }
	if verify := LoadConfig(); verify != nil {
		if s, gerr := verify.GetPBSServer(id); gerr == nil && s != nil && normalize(s.CertFingerprint) == normalize(fingerprint) {
			writeDebugLog(fmt.Sprintf("pinFingerprintLocal: fingerprint for %q persisted to disk OK", id))
		} else {
			onDisk := ""
			if s != nil {
				onDisk = s.CertFingerprint
			}
			writeDebugLog(fmt.Sprintf("pinFingerprintLocal: WARNING fingerprint for %q not on disk after save (on-disk=%q) — config.json unwritable or overwritten", id, onDisk))
		}
	}
	return nil
}

// PinServerFingerprint is the local-API entrypoint the service exposes so the
// unprivileged GUI can pin a fingerprint through the privileged service process,
// keeping the service the single writer of config.json. It performs the write in
// this (service) process.
func (a *App) PinServerFingerprint(id, fingerprint string) error {
	writeDebugLog(fmt.Sprintf("PinServerFingerprint(%s) called (service-side write)", id))
	return a.pinFingerprintLocal(id, fingerprint)
}

// SavePBSServerFromMap is the service-side write for a delegated PBS server
// upsert. It converts the map to a PBSServer, preserves the stored secret when
// the GUI sent an empty one (M-04 parity), and adds or updates by id.
func (a *App) SavePBSServerFromMap(server map[string]interface{}) error {
	jsonData, err := json.Marshal(server)
	if err != nil {
		return fmt.Errorf("failed to marshal pbs server data: %w", err)
	}
	var pbs PBSServer
	if err := json.Unmarshal(jsonData, &pbs); err != nil {
		return fmt.Errorf("failed to unmarshal pbs server data: %w", err)
	}
	existing, _ := a.config.GetPBSServer(pbs.ID)
	if pbs.Secret == "" && existing != nil {
		pbs.Secret = existing.Secret
	}
	writeDebugLog(fmt.Sprintf("SavePBSServerFromMap(%s) called (service-side write)", pbs.ID))
	if existing != nil {
		return a.config.UpdatePBSServer(&pbs)
	}
	return a.config.AddPBSServer(&pbs)
}

// DeletePBSServerByID is the service-side write for a delegated PBS server delete.
func (a *App) DeletePBSServerByID(id string) error {
	writeDebugLog(fmt.Sprintf("DeletePBSServerByID(%s) called (service-side write)", id))
	return a.config.DeletePBSServer(id)
}

// SetDefaultPBSByID is the service-side write for a delegated default-server set.
func (a *App) SetDefaultPBSByID(id string) error {
	writeDebugLog(fmt.Sprintf("SetDefaultPBSByID(%s) called (service-side write)", id))
	return a.config.SetDefaultPBS(id)
}

// SaveConfigFromMap is the service-side write for a delegated full-config save. It
// preserves stored secrets when the GUI sent empty values (M-04 parity), then
// validates and persists.
func (a *App) SaveConfigFromMap(configData map[string]interface{}) error {
	jsonData, err := json.Marshal(configData)
	if err != nil {
		return fmt.Errorf("failed to marshal config data: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(jsonData, &cfg); err != nil {
		return fmt.Errorf("failed to unmarshal config data: %w", err)
	}
	if a.config != nil {
		if cfg.Secret == "" {
			cfg.Secret = a.config.Secret
		}
		if cfg.SMTPPassword == "" {
			cfg.SMTPPassword = a.config.SMTPPassword
		}
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	writeDebugLog("SaveConfigFromMap() called (service-side write)")
	if err := cfg.Save(); err != nil {
		return err
	}
	a.config = &cfg
	return nil
}
