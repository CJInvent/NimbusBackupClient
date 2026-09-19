//go:build service

package main

import (
	"controlplane"
	"errors"
	"fmt"
)

func (a *App) storageBackupTargets(targets []string) ([]string, error) {
	st := a.refreshStorageIdentity()
	if st.Error != "" {
		return nil, fmt.Errorf("%s :: %s", errStorageIdentity, st.Error)
	}
	s, err := a.storageController()
	if err != nil {
		return nil, err
	}
	disks, err := s.Resolve(targets)
	if err != nil {
		return nil, fmt.Errorf("%s :: %w", errStorageIdentity, err)
	}
	paths := make([]string, 0, len(disks))
	for _, d := range disks {
		paths = append(paths, d.Path)
	}
	return paths, nil
}

// Capture the approved baseline before opening the source. Every real handle is
// checked again; a path that changed devices cannot inherit this approval.
func (a *App) storageSourceValidator() (func(uintptr, string) error, error) {
	s, err := a.storageController()
	if err != nil {
		return nil, err
	}
	state := s.Snapshot()
	expected := map[string]controlplane.StorageDevice{}
	for _, binding := range state.Bindings {
		d, e := controlplane.ResolveStorageBinding(binding, state.Devices)
		if e != nil {
			return nil, e
		}
		expected[d.Path] = d
	}
	return func(handle uintptr, path string) error {
		d, ok := expected[path]
		var failure error
		if !ok {
			failure = errors.New("opened source was not approved")
		} else {
			failure = verifyOpenedStorageDevice(handle, d)
		}
		if failure != nil {
			if err := s.Refuse(failure.Error()); err != nil {
				return fmt.Errorf("storage refusal could not be persisted: %w", err)
			}
			return fmt.Errorf("%s :: %w", errStorageIdentity, failure)
		}
		return nil
	}, nil
}
