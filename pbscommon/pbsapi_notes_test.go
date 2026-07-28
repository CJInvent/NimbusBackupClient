package pbscommon

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestSetSnapshotNotes verifies the exact request PBS's notes endpoint
// needs: PUT, the right path, the confirmed-correct colon-separated auth
// header (see PbsApi.php's own regression fix on the server side for why
// that separator matters), and every query param PBS expects to identify
// the snapshot plus the notes text itself -- this is how the Backup Job ID
// gets attached to a PBS snapshot.
func TestSetSnapshotNotes(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotQuery url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	pbs := &PBSClient{
		BaseURL: srv.URL, Datastore: "ds1", Namespace: "acme/sub",
		AuthID: "nimbus@pbs!ctrl", Secret: "s3cr3t",
	}
	jobID := "11111111-2222-4333-8444-555555555555"
	err := pbs.SetSnapshotNotes("host", "DESKTOP-ITFKUD1", 1700000123, "nimbus-job:"+jobID)
	if err != nil {
		t.Fatalf("SetSnapshotNotes: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api2/json/admin/datastore/ds1/notes" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "PBSAPIToken=nimbus@pbs!ctrl:s3cr3t" {
		t.Errorf("Authorization header = %q, want the colon-separated authid:secret format", gotAuth)
	}
	wantQuery := map[string]string{
		"backup-type": "host",
		"backup-id":   "DESKTOP-ITFKUD1",
		"backup-time": "1700000123",
		"ns":          "acme/sub",
		"notes":       "nimbus-job:" + jobID,
	}
	for k, want := range wantQuery {
		if got := gotQuery.Get(k); got != want {
			t.Errorf("query[%s] = %q, want %q", k, got, want)
		}
	}
}

// TestSetSnapshotNotesReturnsErrorOnHTTPFailure: a non-2xx response must
// surface as a Go error, not be silently swallowed -- callers (see the
// backup engines' success paths) rely on this to log-and-continue rather
// than assume the note was set.
func TestSetSnapshotNotesReturnsErrorOnHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer srv.Close()

	pbs := &PBSClient{BaseURL: srv.URL, Datastore: "ds1", AuthID: "a", Secret: "b"}
	err := pbs.SetSnapshotNotes("host", "H", 1, "note")
	if err == nil {
		t.Fatal("expected an error on HTTP 500, got nil")
	}
}
