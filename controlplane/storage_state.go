package controlplane

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// StorageState owns the durable latch and baseline. Callers must authorize
// approvals before invoking Approve. Observations can set, never clear, errors.
type StorageState struct {
	mu      sync.Mutex
	path    string
	status  StorageStatus
	protect func(string) error
}

func OpenStorageState(path string, protect func(string) error) (*StorageState, error) {
	s := &StorageState{path: path, protect: protect}
	if _, err := os.Stat(path); err == nil && protect != nil {
		if err := protect(path); err != nil {
			return nil, fmt.Errorf("storage state ACL: %w", err)
		}
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &s.status); err != nil {
		return nil, fmt.Errorf("storage state unreadable: %w", err)
	}
	return s, nil
}
func (s *StorageState) persist(next StorageStatus) error {
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".storage-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	err = f.Chmod(0600)
	if err == nil && s.protect != nil {
		err = s.protect(name)
	}
	if err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, s.path); err != nil {
		return err
	}
	// Decode the persisted representation so callers retain no mutable aliases.
	var saved StorageStatus
	if err := json.Unmarshal(b, &saved); err != nil {
		return err
	}
	s.status = saved
	return nil
}
func (s *StorageState) Snapshot() StorageStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	// No shared backing slices may escape this lock.
	b, _ := json.Marshal(s.status)
	var cp StorageStatus
	_ = json.Unmarshal(b, &cp)
	return cp
}
func (s *StorageState) Observe(devices []StorageDevice, discoveryErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.status
	next.Devices = devices
	next.Observation = StorageObservation(devices)
	if discoveryErr != nil {
		next.Error = "storage identity discovery failed: " + discoveryErr.Error()
	} else if next.Error == "" {
		for _, binding := range next.Bindings {
			if _, err := ResolveStorageBinding(binding, devices); err != nil {
				next.Error = err.Error()
				break
			}
		}
	}
	return s.persist(next)
}
func (s *StorageState) Resolve(targets []string) ([]StorageDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Error != "" {
		return nil, errors.New(s.status.Error)
	}
	byTarget := map[string]StorageBinding{}
	for _, b := range s.status.Bindings {
		byTarget[b.Target] = b
	}
	var out []StorageDevice
	seen := map[string]bool{}
	var failure error
	if len(targets) == 0 {
		failure = errors.New("storage targets require explicit selection")
	}
	for _, target := range targets {
		b, ok := byTarget[target]
		if !ok {
			failure = fmt.Errorf("storage target %q requires manual binding approval", target)
			break
		}
		d, err := ResolveStorageBinding(b, s.status.Devices)
		if err != nil {
			failure = err
			break
		}
		if !seen[d.ID] {
			out = append(out, d)
			seen[d.ID] = true
		}
	}
	if failure != nil {
		next := s.status
		next.Error = failure.Error()
		if err := s.persist(next); err != nil {
			return nil, fmt.Errorf("cannot persist storage refusal: %w", err)
		}
		return nil, failure
	}
	return out, nil
}
func (s *StorageState) Approve(approval StorageApproval) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateStorageApproval(approval, s.status); err != nil {
		return err
	}
	next := s.status
	next.Bindings = approval.Bindings
	next.Revision = approval.Revision
	next.Error = ""
	return s.persist(next)
}

// Refuse records an error detected on an opened source (including hot-plug).
func (s *StorageState) Refuse(reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.status
	next.Error = reason
	return s.persist(next)
}
