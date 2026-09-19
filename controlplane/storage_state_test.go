package controlplane

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStorageMismatchPersistsUntilExplicitApproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	s, err := OpenStorageState(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	original := storageFixture()
	if err = s.Observe([]StorageDevice{original}, nil); err != nil {
		t.Fatal(err)
	}
	approve := StorageApproval{Revision: 1, Observation: s.Snapshot().Observation, Bindings: []StorageBinding{{Target: "boot", Device: original}}}
	if err = s.Approve(approve); err != nil {
		t.Fatal(err)
	}
	replacement := storageFixture()
	replacement.ID = "replacement-hardware"
	if err = s.Observe([]StorageDevice{replacement}, nil); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Error == "" {
		t.Fatal("replacement not latched")
	}
	s, err = OpenStorageState(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Observe([]StorageDevice{original}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve([]string{"boot"}); err == nil {
		t.Fatal("reconnecting disk cleared unresolved error")
	}
	approve.Revision = 2
	approve.Observation = s.Snapshot().Observation
	if err = s.Approve(approve); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve([]string{"boot"}); err != nil {
		t.Fatal(err)
	}
}
func TestStorageBaselineIsNotOverwrittenByObservation(t *testing.T) {
	s, err := OpenStorageState(filepath.Join(t.TempDir(), "storage.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	original := storageFixture()
	if err = s.Observe([]StorageDevice{original}, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Approve(StorageApproval{Revision: 1, Observation: s.Snapshot().Observation, Bindings: []StorageBinding{{Target: "boot", Device: original}}}); err != nil {
		t.Fatal(err)
	}
	changed := storageFixture()
	changed.DiskID = "other"
	if err = s.Observe([]StorageDevice{changed}, nil); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Bindings[0].Device.DiskID != original.DiskID {
		t.Fatal("observation changed approved baseline")
	}
}

func TestStorageStateRefusesUnprotectedPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	s, err := OpenStorageState(path, func(name string) error {
		fi, e := os.Stat(name)
		if e != nil {
			t.Fatal(e)
		}
		if fi.Size() != 0 {
			t.Fatal("baseline written before ACL protection")
		}
		return errors.New("ACL refused")
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Observe([]StorageDevice{storageFixture()}, nil) == nil {
		t.Fatal("unprotected baseline persisted")
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed persistence exposed state file")
	}
	if s.Snapshot().Observation != "" {
		t.Fatal("failed persistence changed authoritative memory")
	}
}

func TestStorageAuthorityChangeInvalidatesLocalApproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	s, err := OpenStorageState(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetAuthority("standalone"); err != nil {
		t.Fatal(err)
	}
	d := storageFixture()
	if err = s.Observe([]StorageDevice{d}, nil); err != nil {
		t.Fatal(err)
	}
	approval := StorageApproval{Revision: 20, Observation: s.Snapshot().Observation, Bindings: []StorageBinding{{Target: "boot", Device: d}}}
	if err = s.Approve(approval); err != nil {
		t.Fatal(err)
	}
	if err = s.SetAuthority("joined:server-a-agent-1"); err != nil {
		t.Fatal(err)
	}
	st := s.Snapshot()
	if st.Revision != 0 || len(st.Bindings) != 0 || st.Error == "" {
		t.Fatal("local approval crossed authority boundary")
	}
	s, err = OpenStorageState(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve([]string{"boot"}); err == nil {
		t.Fatal("restarted joined agent used local approval")
	}
	approval.Revision = 1
	if err = s.Approve(approval); err != nil {
		t.Fatalf("new dashboard revision blocked by old local revision: %v", err)
	}
	if err = s.SetAuthority("joined:server-a-agent-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve([]string{"boot"}); err != nil {
		t.Fatal("unchanged authority invalidated dashboard approval")
	}
	if err = s.SetAuthority("joined:server-a-agent-2"); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Error == "" || len(s.Snapshot().Bindings) != 0 {
		t.Fatal("re-enrollment retained prior authority")
	}
}
