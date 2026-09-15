package controlplane

import (
	"encoding/json"
	"testing"
)

// The wire contract for a run report — docs/V4-RUN-AUDIT.md sections 1 and 2.
//
// The property under test is that a MEASUREMENT of zero survives the wire.
// BytesTotal, BytesUploaded and PBSBackupTime were plain int64 with omitempty,
// and a fully deduplicated backup uploads exactly zero bytes — the steady
// state for most machines on most nights. The field vanished from the JSON,
// the server stored NULL, and the portal showed a dash. "Nothing changed" and
// "never reported" became indistinguishable, and the case it erased was the
// ordinary one rather than an edge.
//
// The identity fields are asserted here too, because the failure they fix is
// also a wire-level one: the client knew the trigger and the job id and simply
// never sent them.

func decode(t *testing.T, r RunReport) map[string]any {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestZeroMeasurementSurvivesTheWire(t *testing.T) {
	zero := int64(0)
	m := decode(t, RunReport{
		Status:        StatusSuccess,
		BytesTotal:    &zero,
		BytesUploaded: &zero,
		PBSBackupTime: &zero,
	})

	for _, field := range []string{"bytes_total", "bytes_uploaded", "pbs_backup_time"} {
		v, present := m[field]
		if !present {
			t.Fatalf("%s was omitted; a measured zero must reach the server", field)
		}
		if v == nil {
			t.Fatalf("%s marshalled as null; null means NOT KNOWN, and this was measured", field)
		}
		if n, ok := v.(float64); !ok || n != 0 {
			t.Fatalf("%s = %v, want 0", field, v)
		}
	}
}

func TestUnknownMeasurementIsNullNotAbsent(t *testing.T) {
	// Preparing() legitimately knows no byte counts yet. That must be
	// distinguishable from "measured, and it was zero" — which is the whole
	// reason these fields are pointers rather than plain integers.
	m := decode(t, RunReport{Status: StatusPreparing})

	for _, field := range []string{"bytes_total", "bytes_uploaded", "pbs_backup_time"} {
		v, present := m[field]
		if !present {
			t.Fatalf("%s absent; the field must be sent so null can mean not-known", field)
		}
		if v != nil {
			t.Fatalf("%s = %v, want null for a report that has not measured yet", field, v)
		}
	}
}

func TestTriggerIsAlwaysSent(t *testing.T) {
	// Never omitempty. The server stores an absent trigger as "service", and
	// silently defaulting is how it came to infer four states from whether a
	// request_id happened to be set.
	m := decode(t, RunReport{Status: StatusPreparing})
	if _, present := m["trigger"]; !present {
		t.Fatal("trigger was omitted; it is sent on every report, even when empty")
	}

	m = decode(t, RunReport{Status: StatusPreparing, Trigger: "portal"})
	if m["trigger"] != "portal" {
		t.Fatalf("trigger = %v, want portal", m["trigger"])
	}
}

func TestJobIDIsOmittedOnlyWhenThereIsNone(t *testing.T) {
	// Asymmetric with the measurements ABOVE, deliberately: an identity is
	// either present or it is not, and zero is not a job id.
	m := decode(t, RunReport{Status: StatusPreparing})
	if _, present := m["job_id"]; present {
		t.Fatal("job_id sent for a run that belongs to no managed job")
	}

	m = decode(t, RunReport{Status: StatusPreparing, JobID: 42})
	if m["job_id"] != float64(42) {
		t.Fatalf("job_id = %v, want 42", m["job_id"])
	}
}

func TestReporterSetsIdentityBeforeTheFirstPost(t *testing.T) {
	// A run that never reaches a terminal state must still be able to say what
	// started it and which job it belongs to — those are exactly the runs an
	// operator needs to identify.
	r := (&Client{}).NewRun("Nightly", "directory")
	r.SetTrigger("schedule")
	r.SetJobID(7)

	if r.base.Trigger != "schedule" {
		t.Fatalf("trigger = %q, want schedule", r.base.Trigger)
	}
	if r.base.JobID != 7 {
		t.Fatalf("job_id = %d, want 7", r.base.JobID)
	}
	if r.base.RunUUID == "" {
		t.Fatal("a run must carry its own uuid from the first report")
	}
}
