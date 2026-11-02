package main

// The console's bindings (docs/V4-CONSOLE.md §3 and §5, phase C2).
//
// THE PROPERTY: every binding is one request to the service, the expected one,
// and nothing else. A fake service records each request; each case calls one
// binding and compares the recording with what §3 says it asks for. A binding
// that reads a file, dials PBS or calls two routes fails here.
//
// THE CASE TABLE COVERS EVERY BOUND METHOD. A method added to Console without
// a case fails TestEveryBindingHasARouteCase, so the property cannot quietly
// stop being checked for a new binding.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// call is one request the fake service received. Op is the restore op named
// in the body, for the three restore routes.
type call struct{ Method, Path, Op string }

func (c call) String() string {
	if c.Op != "" {
		return c.Method + " " + c.Path + " op=" + c.Op
	}
	return c.Method + " " + c.Path
}

// fakeService answers every route the console uses with a fixed, valid body,
// and records what it was asked.
type fakeService struct {
	mu    sync.Mutex
	calls []call
	// pbsTest is POST /pbs/test's answer.
	pbsTest api.PBSTestResponse
}

func (f *fakeService) take() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

func (f *fakeService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c := call{Method: r.Method, Path: r.URL.Path}
	if r.URL.RawQuery != "" {
		c.Path += "?" + r.URL.RawQuery
	}
	var body struct {
		Op string `json:"op"`
	}
	raw, _ := io.ReadAll(r.Body)
	if strings.HasPrefix(r.URL.Path, "/restore/") && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
		c.Op = body.Op
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	pbsTest := f.pbsTest
	f.mu.Unlock()

	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.URL.Path == "/status":
		reply(api.StatusResponse{Running: true, Version: "1.01.00",
			Configuration:    map[string]any{"hostname": "WS-01", "last_backup_dirs": []string{`C:\Users`}},
			SecurityWarnings: []string{"Windows Update last succeeded 90 days ago"}})
	case r.URL.Path == "/controlplane/status":
		reply(map[string]any{"configured": true, "gui_read_only": true, "storage_identity": map[string]any{"error": ""}})
	case r.URL.Path == "/storage/identity" && r.Method == http.MethodGet:
		reply(map[string]any{"devices": []any{}})
	case r.URL.Path == "/exchange":
		out := api.ExchangeResponse{Status: api.ExchangeStatus{Installed: true, Version: "2019"}}
		if r.URL.Query().Get("log_mode") == "1" {
			out.LogMode = &api.ExchangeLogMode{Queried: true, LogsAccumulate: true}
		}
		reply(out)
	case r.URL.Path == "/disks":
		reply(api.DisksResponse{Disks: []api.PhysicalDisk{{Identity: "serial:A", Letters: []string{"C:"}}}})
	case r.URL.Path == "/connections":
		reply(api.ConnectionsResponse{PBS: []api.PBSConnection{}, ServerTime: now})
	case r.URL.Path == "/runs/active" || r.URL.Path == "/runs/recent":
		reply(api.RunsResponse{Runs: []api.Run{}, ServerTime: now})
	case r.URL.Path == "/backup":
		reply(api.BackupResponse{Success: true, JobID: "run-1-1"})
	case r.URL.Path == "/jobs":
		reply(api.JobsResponse{Jobs: []api.ScheduledJob{{ID: "job-1", Name: "Nightly", ScheduleTime: "02:00"}}})
	case r.URL.Path == "/pbs/servers":
		reply(api.PBSServersResponse{Servers: []api.PBSServerInfo{{ID: "pbs1", SecretSet: true}}, DefaultID: "pbs1"})
	case r.URL.Path == "/pbs/test":
		reply(pbsTest)
	case r.URL.Path == "/pbs/discover":
		reply(api.PBSFingerprintResponse{Fingerprint: "AA:BB"})
	case r.URL.Path == "/restore/query":
		var result any = map[string]any{}
		switch body.Op {
		case api.RestoreOpSnapshots, api.RestoreOpContents, api.RestoreOpVolumePartitions, api.RestoreOpVolumeDirectory:
			result = []any{}
		case api.RestoreOpVolumeFiles:
			result = api.VolumeFilesResult{Entries: []api.SnapshotEntry{}}
		case api.RestoreOpSpace:
			result = api.SpaceCheck{Fits: true, FreeBytes: 100, TotalBytes: 200, NeededBytes: 1}
		}
		b, _ := json.Marshal(result)
		reply(map[string]json.RawMessage{"result": b})
	case r.URL.Path == "/restore/job":
		reply(map[string]string{"job_id": "rj-1"})
	case strings.HasPrefix(r.URL.Path, "/restore/job/"):
		reply(api.RestoreJobState{ID: "rj-1", Complete: true, Success: true, Result: json.RawMessage(`{}`)})
	default:
		// Every write: /backup/cancel, /jobs/*, /pbs/save|default|fingerprint|delete,
		// /controlplane/save, POST /storage/identity, /restore/control.
		reply(map[string]any{"success": true, "ok": true})
	}
}

// newTestConsole is a console whose service is the fake, already found.
func newTestConsole(t *testing.T) (*Console, *fakeService) {
	t.Helper()
	f := &fakeService{pbsTest: api.PBSTestResponse{OK: true}}
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	c := &Console{api: api.NewClientAt(ts.URL)}
	if _, err := c.service(); err != nil {
		t.Fatalf("the fake service was not found: %v", err)
	}
	f.take() // the probe
	return c, f
}

// bindingCase calls one binding. want is the requests it must make, in order;
// asyncPolls marks a job op whose state may be polled more than once.
type bindingCase struct {
	call       func(c *Console) error
	want       []call
	asyncPolls bool
}

func get(path string) call   { return call{Method: http.MethodGet, Path: path} }
func post(path string) call  { return call{Method: http.MethodPost, Path: path} }
func del(path string) call   { return call{Method: http.MethodDelete, Path: path} }
func query(op string) call   { return call{Method: http.MethodPost, Path: "/restore/query", Op: op} }
func control(op string) call { return call{Method: http.MethodPost, Path: "/restore/control", Op: op} }
func job(op string) []call {
	return []call{{http.MethodPost, "/restore/job", op}, get("/restore/job/rj-1")}
}
func first[T any](_ T, err error) error { return err }

var bindingCases = map[string]bindingCase{
	// The console's own facts: no request at all.
	"GetVersion":              {call: func(c *Console) error { c.GetVersion(); return nil }},
	"SetTrayLanguage":         {call: func(c *Console) error { c.SetTrayLanguage("en"); return nil }},
	"LastVolumeListTruncated": {call: func(c *Console) error { c.LastVolumeListTruncated(); return nil }},
	// The folder picker lists as the user until V4-LOCAL-API P3.
	"ListDrives":  {call: func(c *Console) error { c.ListDrives(); return nil }},
	"ListFolders": {call: func(c *Console) error { return first(c.ListFolders(os.TempDir())) }},
	"CreateFolder": {call: func(c *Console) error {
		dir, err := os.MkdirTemp("", "nimbus-console-test-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		return first(c.CreateFolder(dir, "new"))
	}},
	"DefaultSaveDir": {call: func(c *Console) error { c.DefaultSaveDir(); return nil }},

	// The machine and its configuration.
	"GetSystemInfo":         {call: func(c *Console) error { c.GetSystemInfo(); return nil }, want: []call{get("/status")}},
	"GetConfigWithHostname": {call: func(c *Console) error { return first(c.GetConfigWithHostname()) }, want: []call{get("/status")}},
	"GetLastBackupDirs":     {call: func(c *Console) error { return first(c.GetLastBackupDirs()) }, want: []call{get("/status")}},
	"GetSecurityWarnings":   {call: func(c *Console) error { return first(c.GetSecurityWarnings()) }, want: []call{get("/status")}},
	"GetExchangeStatus":     {call: func(c *Console) error { return first(c.GetExchangeStatus()) }, want: []call{get("/exchange")}},
	"QueryExchangeLogMode":  {call: func(c *Console) error { return first(c.QueryExchangeLogMode()) }, want: []call{get("/exchange?log_mode=1")}},
	"ListPhysicalDisks":     {call: func(c *Console) error { return first(c.ListPhysicalDisks()) }, want: []call{get("/disks")}},

	// The control server, the lock and the storage identity.
	"IsReadOnly":             {call: func(c *Console) error { c.IsReadOnly(); return nil }, want: []call{get("/controlplane/status")}},
	"GetControlServerStatus": {call: func(c *Console) error { return first(c.GetControlServerStatus()) }, want: []call{get("/controlplane/status")}},
	"SaveControlServerConfig": {call: func(c *Console) error {
		return c.SaveControlServerConfig("https://control.example", "", "")
	}, want: []call{post("/controlplane/save")}},
	"GetStorageIdentityStatus": {call: func(c *Console) error { return first(c.GetStorageIdentityStatus()) }, want: []call{get("/storage/identity")}},
	"ApproveStorageIdentity": {call: func(c *Console) error {
		return c.ApproveStorageIdentity(map[string]interface{}{"revision": 1})
	}, want: []call{post("/storage/identity")}},

	// Runs.
	"GetConnections": {call: func(c *Console) error { return first(c.GetConnections()) }, want: []call{get("/connections")}},
	"GetActiveRuns":  {call: func(c *Console) error { return first(c.GetActiveRuns()) }, want: []call{get("/runs/active")}},
	"GetRecentRuns":  {call: func(c *Console) error { return first(c.GetRecentRuns(7)) }, want: []call{get("/runs/recent?days=7")}},
	"StartBackup": {call: func(c *Console) error {
		return c.StartBackup("directory", []string{`C:\Users`}, nil, nil, "WS-01", true, "fastest")
	}, want: []call{post("/backup")}},
	"StopBackup": {call: func(c *Console) error { return c.StopBackup() }, want: []call{post("/backup/cancel")}},

	// Local scheduled jobs.
	"GetScheduledJobs": {call: func(c *Console) error { return first(c.GetScheduledJobs()) }, want: []call{get("/jobs")}},
	"SaveScheduledJob": {call: func(c *Console) error {
		return c.SaveScheduledJob(api.ScheduledJob{ID: "job-2", Name: "Nightly", ScheduleTime: "02:00"})
	}, want: []call{post("/jobs/create")}},
	"UpdateScheduledJob": {call: func(c *Console) error {
		return c.UpdateScheduledJob(api.ScheduledJob{ID: "job-1", Name: "Nightly", ScheduleTime: "03:00"})
	}, want: []call{post("/jobs/update")}},
	"DeleteScheduledJob": {call: func(c *Console) error { return c.DeleteScheduledJob("job-1") }, want: []call{del("/jobs/delete/job-1")}},

	// PBS servers.
	"ListPBSServers":  {call: func(c *Console) error { return first(c.ListPBSServers()) }, want: []call{get("/pbs/servers")}},
	"GetDefaultPBSID": {call: func(c *Console) error { return first(c.GetDefaultPBSID()) }, want: []call{get("/pbs/servers")}},
	"AddPBSServer": {call: func(c *Console) error {
		return c.AddPBSServer(api.PBSServerSave{ID: "pbs2", Name: "New", BaseURL: "https://pbs2.example:8007"})
	}, want: []call{post("/pbs/save")}},
	"UpdatePBSServer": {call: func(c *Console) error {
		return c.UpdatePBSServer(api.PBSServerSave{ID: "pbs1", Name: "Primary", BaseURL: "https://pbs1.example:8007"})
	}, want: []call{post("/pbs/save")}},
	"DeletePBSServer":         {call: func(c *Console) error { return c.DeletePBSServer("pbs1") }, want: []call{del("/pbs/delete/pbs1")}},
	"SetDefaultPBSServer":     {call: func(c *Console) error { return c.SetDefaultPBSServer("pbs1") }, want: []call{post("/pbs/default")}},
	"TestPBSConnection":       {call: func(c *Console) error { return c.TestPBSConnection("pbs1") }, want: []call{post("/pbs/test")}},
	"GetServerFingerprint":    {call: func(c *Console) error { return first(c.GetServerFingerprint("https://pbs1.example:8007")) }, want: []call{post("/pbs/discover")}},
	"PinPBSServerFingerprint": {call: func(c *Console) error { return c.PinPBSServerFingerprint("pbs1", "AA:BB") }, want: []call{post("/pbs/fingerprint")}},

	// Restore: three routes and the op table (gui/api/restore.go).
	"CheckDownloadSpace":   {call: func(c *Console) error { return first(c.CheckDownloadSpace(`D:\Restore`, 1)) }, want: []call{query(api.RestoreOpSpace)}},
	"ListSnapshots":        {call: func(c *Console) error { return first(c.ListSnapshots("pbs1", "WS-01")) }, want: []call{query(api.RestoreOpSnapshots)}},
	"ListSnapshotContents": {call: func(c *Console) error { return first(c.ListSnapshotContents("pbs1", "WS-01", 1, false)) }, want: []call{query(api.RestoreOpContents)}},
	"GetSnapshotMeta":      {call: func(c *Console) error { return first(c.GetSnapshotMeta("pbs1", "WS-01", 1)) }, want: []call{query(api.RestoreOpMeta)}},
	"ListVolumePartitions": {call: func(c *Console) error {
		return first(c.ListVolumePartitions("pbs1", "WS-01", "2026-10-18T02:00:00Z", "vm", "drive-scsi0.img.fidx"))
	}, want: []call{query(api.RestoreOpVolumePartitions)}},
	"ListVolumeFiles": {call: func(c *Console) error {
		return first(c.ListVolumeFiles("pbs1", "WS-01", "2026-10-18T02:00:00Z", "vm", "drive-scsi0.img.fidx", 0, false))
	}, want: []call{query(api.RestoreOpVolumeFiles)}},
	"ListVolumeDirectory": {call: func(c *Console) error {
		return first(c.ListVolumeDirectory("pbs1", "WS-01", "2026-10-18T02:00:00Z", "vm", "drive-scsi0.img.fidx", 0, `\Users`))
	}, want: []call{query(api.RestoreOpVolumeDirectory)}},
	"CancelSearch":            {call: func(c *Console) error { c.CancelSearch(); return nil }, want: []call{control(api.RestoreOpCancelSearch)}},
	"CancelVolumeFileRestore": {call: func(c *Console) error { c.CancelVolumeFileRestore(); return nil }, want: []call{control(api.RestoreOpCancelVolumeFileRestore)}},
	"SearchFiles": {call: func(c *Console) error {
		return first(c.SearchFiles("pbs1", "WS-01", "report", "name", 0, 0, false))
	}, want: job(api.RestoreOpSearch), asyncPolls: true},
	"DownloadSelection": {call: func(c *Console) error {
		return c.DownloadSelection("pbs1", "WS-01", "2026-10-18T02:00:00Z", []string{"a.txt"}, `D:\Restore`, true, 1)
	}, want: job(api.RestoreOpDownload), asyncPolls: true},
	"RestoreSnapshot": {call: func(c *Console) error {
		return c.RestoreSnapshot("pbs1", "WS-01", "2026-10-18T02:00:00Z", `D:\Restore`, "alternate", nil, false, false, false, true, false)
	}, want: job(api.RestoreOpRestore), asyncPolls: true},
	"DownloadFilesFromVolume": {call: func(c *Console) error {
		return c.DownloadFilesFromVolume("pbs1", "WS-01", "2026-10-18T02:00:00Z", "vm", "drive-scsi0.img.fidx", 0, []string{`\a.txt`}, `D:\Restore`, true, 1)
	}, want: job(api.RestoreOpVolumeDownload), asyncPolls: true},
	"RestoreFilesFromVolume": {call: func(c *Console) error {
		return c.RestoreFilesFromVolume("pbs1", "WS-01", "2026-10-18T02:00:00Z", "vm", "drive-scsi0.img.fidx", 0, []string{`\a.txt`}, `D:\Restore`, true, false, true, false, false, 1)
	}, want: job(api.RestoreOpVolumeFileRestore), asyncPolls: true},
}

func TestEveryBindingMakesExactlyItsOwnRequest(t *testing.T) {
	for name, bc := range bindingCases {
		t.Run(name, func(t *testing.T) {
			c, f := newTestConsole(t)
			if err := bc.call(c); err != nil {
				t.Fatalf("%s against a healthy service: %v", name, err)
			}
			got := f.take()
			if bc.asyncPolls {
				// RestoreSnapshot and the volume jobs return before the job
				// ends; wait for the first poll, then require that nothing
				// but polls of that job followed the start.
				deadline := time.Now().Add(5 * time.Second)
				for len(got) < 2 && time.Now().Before(deadline) {
					time.Sleep(20 * time.Millisecond)
					got = append(got, f.take()...)
				}
				if len(got) >= 2 {
					for _, extra := range got[2:] {
						if extra != got[1] {
							t.Errorf("%s made %v after its job poll", name, extra)
						}
					}
					got = got[:2]
				}
			}
			if !reflect.DeepEqual(got, bc.want) {
				t.Errorf("%s asked the service for %v; want %v", name, got, bc.want)
			}
		})
	}
}

// boundMethods is Console's exported method set: what Wails binds.
func boundMethods() []string {
	t := reflect.TypeOf(&Console{})
	out := make([]string, 0, t.NumMethod())
	for i := 0; i < t.NumMethod(); i++ {
		out = append(out, t.Method(i).Name)
	}
	sort.Strings(out)
	return out
}

func TestEveryBindingHasARouteCase(t *testing.T) {
	bound := boundMethods()
	if len(bound) < 40 {
		t.Fatalf("Console binds %d methods; the method set looks wrong, and a short list proves nothing", len(bound))
	}
	for _, m := range bound {
		if _, ok := bindingCases[m]; !ok {
			t.Errorf("Console.%s is bound but has no case in bindingCases: say which request it makes", m)
		}
	}
	for name := range bindingCases {
		if !hasName(bound, name) {
			t.Errorf("bindingCases has %q, which Console does not bind", name)
		}
	}
}

// bindingsJS is the names the front end reads from the bound type in
// frontend/src/lib/bindings.js: `export const X = app.X`.
func bindingsJS(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("frontend", "src", "lib", "bindings.js"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^export const (\w+) = app\.(\w+)\s*$`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		if m[1] != m[2] {
			t.Errorf("bindings.js exports %s as app.%s; the names must match", m[1], m[2])
		}
		out = append(out, m[2])
	}
	sort.Strings(out)
	return out
}

func TestTheBoundSurfaceIsExactlyWhatTheFrontEndUses(t *testing.T) {
	// Both directions (docs/V4-CONSOLE.md §5 item 2): a method the front end
	// does not use is a binding nobody needs, which is how 31 of them,
	// StartControlPlane among them, ended up callable from the webview; a name
	// the front end uses that the console lacks is a broken screen.
	bound, used := boundMethods(), bindingsJS(t)
	if len(used) < 40 {
		t.Fatalf("read %d names from bindings.js; the pattern no longer matches the file", len(used))
	}
	for _, m := range bound {
		if !hasName(used, m) {
			t.Errorf("Console.%s is bound but bindings.js does not export it", m)
		}
	}
	for _, m := range used {
		if !hasName(bound, m) {
			t.Errorf("bindings.js reads app.%s, which Console does not bind", m)
		}
	}
	// And the front end reads the bound type by its name.
	src, _ := os.ReadFile(filepath.Join("frontend", "src", "lib", "bindings.js"))
	if !strings.Contains(string(src), "window.go.main.Console") {
		t.Error("bindings.js does not read window.go.main.Console, the type Wails binds")
	}
}

func TestTheServiceTypeIsNotBound(t *testing.T) {
	o := guiOptions(&Console{}, false, t.TempDir())
	if len(o.Bind) != 1 {
		t.Fatalf("Wails binds %d values, want exactly the console", len(o.Bind))
	}
	if _, ok := o.Bind[0].(*Console); !ok {
		t.Fatalf("Wails binds %T, want *Console: every exported method of a bound type is callable from the webview", o.Bind[0])
	}
}

// ---- refusals ----------------------------------------------------------

// writeBindings are the bindings that change something. Each must refuse
// without sending anything when the service cannot be reached.
var writeBindings = []string{
	"SaveControlServerConfig", "ApproveStorageIdentity", "StartBackup", "StopBackup",
	"SaveScheduledJob", "UpdateScheduledJob", "DeleteScheduledJob",
	"AddPBSServer", "UpdatePBSServer", "DeletePBSServer", "SetDefaultPBSServer",
	"TestPBSConnection", "GetServerFingerprint", "PinPBSServerFingerprint",
	"CheckDownloadSpace", "ListSnapshots", "SearchFiles", "DownloadSelection",
}

func TestWritesRefuseWithoutAServiceAndDoNothingElse(t *testing.T) {
	// No local fallback (V4-CONSOLE §2 rule 3): with the service down, the
	// seven writes that used to write config.json themselves, and every
	// other write, return NB-2011.
	ts := httptest.NewServer(http.NotFoundHandler())
	ts.Close() // an address nothing answers on
	for _, name := range writeBindings {
		c := &Console{api: api.NewClientAt(ts.URL)}
		err := bindingCases[name].call(c)
		if err == nil || !strings.Contains(err.Error(), "[NB-2011]") {
			t.Errorf("%s with no service = %v, want NB-2011", name, err)
		}
	}
}

func TestReadsWithoutAServiceAreEmptyOrAnError(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	ts.Close()
	c := &Console{api: api.NewClientAt(ts.URL)}
	if info := c.GetSystemInfo(); info["service_available"] != false || info["hostname"] != "" {
		t.Errorf("GetSystemInfo with no service = %v", info)
	}
	if c.IsReadOnly() {
		t.Error("an unreachable service read as a locked console")
	}
	if _, err := c.GetConfigWithHostname(); err == nil {
		t.Error("GetConfigWithHostname with no service returned a configuration")
	}
	if r, err := c.GetActiveRuns(); err != nil || r == nil || r.Runs == nil {
		t.Errorf("GetActiveRuns with no service = %+v, %v; the poller wants an empty answer", r, err)
	}
}

// lockedHandler is the smallest api.BackupHandler: the test is about the
// server's lock, which refuses before any of these is reached.
type lockedHandler struct {
	mu    sync.Mutex
	calls []string
}

func (h *lockedHandler) note(s string) error {
	h.mu.Lock()
	h.calls = append(h.calls, s)
	h.mu.Unlock()
	return nil
}
func (h *lockedHandler) StartBackup(string, []string, []string, []string, string, bool, string) error {
	return h.note("StartBackup")
}
func (h *lockedHandler) GetConfigWithHostname() map[string]interface{} {
	return map[string]interface{}{}
}
func (h *lockedHandler) ListScheduledJobs() ([]api.ScheduledJob, error) {
	return []api.ScheduledJob{}, nil
}
func (h *lockedHandler) SaveScheduledJobFromMap(map[string]interface{}) error {
	return h.note("SaveScheduledJobFromMap")
}
func (h *lockedHandler) UpdateScheduledJobFromMap(map[string]interface{}) error {
	return h.note("UpdateScheduledJobFromMap")
}
func (h *lockedHandler) DeleteScheduledJobFromMap(string) error {
	return h.note("DeleteScheduledJobFromMap")
}
func (h *lockedHandler) PinServerFingerprint(string, string) error { return h.note("Pin") }
func (h *lockedHandler) SavePBSServerFromMap(map[string]interface{}) error {
	return h.note("SavePBSServerFromMap")
}
func (h *lockedHandler) DeletePBSServerByID(string) error { return h.note("DeletePBSServerByID") }
func (h *lockedHandler) SetDefaultPBSByID(string) error   { return h.note("SetDefaultPBSByID") }
func (h *lockedHandler) SaveConfigFromMap(map[string]interface{}) error {
	return h.note("SaveConfigFromMap")
}
func (h *lockedHandler) ControlPlaneStatusMap() map[string]interface{} {
	return map[string]interface{}{"gui_read_only": true}
}
func (h *lockedHandler) SaveControlPlaneFromMap(map[string]interface{}) error {
	return h.note("SaveControlPlaneFromMap")
}

func TestJobWritesAreRefusedWhileLocked(t *testing.T) {
	// GUI audit finding 3 (S8's test, moved here as V4-CONSOLE C2 says): the
	// job bindings used to write scheduled_jobs.json from the user's session,
	// so the org's lock never saw them. Against the REAL local API server
	// with the lock on, each is refused and the service is never reached.
	h := &lockedHandler{}
	srv := api.NewServer(h)
	srv.SetLockedFunc(func() bool { return true })
	// The pipe gives the service its caller; a test server is given one.
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Config.ConnContext = api.FixedCallerContext(api.Caller{UserSID: "S-1-5-21-1-2-3-1001", SessionID: 1})
	ts.Start()
	t.Cleanup(ts.Close)
	c := &Console{api: api.NewClientAt(ts.URL)}

	for _, name := range []string{"SaveScheduledJob", "UpdateScheduledJob", "DeleteScheduledJob",
		"AddPBSServer", "SaveControlServerConfig", "StartBackup"} {
		err := bindingCases[name].call(c)
		var se *api.StatusError
		if !errors.As(err, &se) || se.Code != http.StatusForbidden || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("%s while locked = %v, want the service's 403", name, err)
		}
	}
	if len(h.calls) != 0 {
		t.Errorf("the locked service ran %v", h.calls)
	}
	if !c.IsReadOnly() {
		t.Error("IsReadOnly is false while the service reports the console locked")
	}

	// Unlocked, the same calls reach the service: the refusal above was the
	// lock's, not a broken route.
	srv.SetLockedFunc(func() bool { return false })
	if err := c.SaveScheduledJob(api.ScheduledJob{ID: "job-2"}); err != nil || len(h.calls) != 1 {
		t.Errorf("unlocked SaveScheduledJob = %v (service calls %v)", err, h.calls)
	}
}

func TestAnUntrustedCertificateComesBackCoded(t *testing.T) {
	// The setup screen offers to pin a fingerprint on NB-1011, which replaced
	// matching "x509" in the error text.
	c, f := newTestConsole(t)
	f.mu.Lock()
	f.pbsTest = api.PBSTestResponse{Error: "x509: certificate signed by unknown authority", CertUntrusted: true}
	f.mu.Unlock()
	err := c.TestPBSConnection("pbs1")
	if err == nil || !strings.HasPrefix(err.Error(), "[NB-1011]") || !strings.Contains(err.Error(), "unknown authority") {
		t.Errorf("an untrusted certificate = %v, want NB-1011 with the TLS detail", err)
	}

	f.mu.Lock()
	f.pbsTest = api.PBSTestResponse{Error: "authentication failed: invalid credentials"}
	f.mu.Unlock()
	err = c.TestPBSConnection("pbs1")
	if err == nil || strings.Contains(err.Error(), "NB-1011") || !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("a refused token = %v, want the PBS message and no NB-1011", err)
	}
}

func TestBindingsCarryTheServicesAnswer(t *testing.T) {
	// Spot checks that what comes back is the service's, not a local default.
	c, _ := newTestConsole(t)
	if info := c.GetSystemInfo(); info["hostname"] != "WS-01" || info["service_available"] != true {
		t.Errorf("GetSystemInfo = %v", info)
	}
	if dirs, _ := c.GetLastBackupDirs(); len(dirs) != 1 || dirs[0] != `C:\Users` {
		t.Errorf("GetLastBackupDirs = %v", dirs)
	}
	if w, _ := c.GetSecurityWarnings(); len(w) != 1 {
		t.Errorf("GetSecurityWarnings = %v", w)
	}
	if lm, _ := c.QueryExchangeLogMode(); !lm.LogsAccumulate {
		t.Errorf("QueryExchangeLogMode = %+v", lm)
	}
	if id, _ := c.GetDefaultPBSID(); id != "pbs1" {
		t.Errorf("GetDefaultPBSID = %q", id)
	}
	if sc, _ := c.CheckDownloadSpace(`D:\Restore`, 1); !sc.Fits || sc.TotalBytes != 200 {
		t.Errorf("CheckDownloadSpace = %+v", sc)
	}
	if jobs, _ := c.GetScheduledJobs(); len(jobs) != 1 || jobs[0].ScheduleTime != "02:00" {
		t.Errorf("GetScheduledJobs = %+v", jobs)
	}
}

func hasName(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
