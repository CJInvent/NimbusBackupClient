package api

import (
	"net/http"
)

// Facts about this machine, answered by the service (docs/V4-CONSOLE.md §4.1).
//
// The console used to establish both itself: it opened \\.\PhysicalDriveN
// from the user's session to list disks, and probed the registry and the
// Exchange Management Shell for Exchange. Both are things the service already
// does for its own purposes (the disk inventory it reports to the control
// server, the post-backup Exchange tasks), so the console asks rather than
// repeating them.
//
//	GET /disks                  the physical disks a machine backup can target
//	GET /exchange[?log_mode=1]  Exchange detection; the log-mode query only
//	                            when asked for, because it runs the Exchange
//	                            Management Shell and takes seconds
//
// These types are the wire shapes and the service's own: the service aliases
// them rather than keeping a twin to convert from.

// PhysicalDisk is one disk a machine backup can target.
type PhysicalDisk struct {
	Identity   string   `json:"identity"`
	Target     string   `json:"target"`
	DiskNumber int      `json:"diskNumber"`
	SizeBytes  int64    `json:"sizeBytes"`
	SizeText   string   `json:"sizeText"`
	Letters    []string `json:"letters"`
	Label      string   `json:"label"`
	Path       string   `json:"path"`
}

// DisksResponse is GET /disks. An object rather than a bare array, so it can
// gain a field without breaking a consumer.
type DisksResponse struct {
	Disks []PhysicalDisk `json:"disks"`
}

// ExchangeStatus is the detection result.
type ExchangeStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"` // e.g. "2019", "2016", or "" when absent
	Aware     bool   `json:"aware"`   // config.ExchangeAware
	// HighlightSetting is true when Exchange is present but app-aware mode is
	// off: the console draws attention to the toggle.
	HighlightSetting bool `json:"highlight_setting"`
	// LogTruncation mirrors config.ExchangeLogTruncation; HighlightTruncation
	// is true when Exchange is present but post-backup log truncation is off.
	LogTruncation       bool `json:"log_truncation"`
	HighlightTruncation bool `json:"highlight_truncation"`
}

// ExchangeLogMode is the transaction-log posture, queried on request so the
// console can recommend truncation only when logs actually accumulate.
type ExchangeLogMode struct {
	Queried        bool   `json:"queried"`
	LogsAccumulate bool   `json:"logs_accumulate"`
	Detail         string `json:"detail"`
}

// ExchangeResponse is GET /exchange. LogMode is present only when the request
// asked for it with log_mode=1.
type ExchangeResponse struct {
	Status  ExchangeStatus   `json:"status"`
	LogMode *ExchangeLogMode `json:"log_mode,omitempty"`
}

// MachineHandler is the service side of these routes. Optional: a server
// whose app lacks it answers 501.
type MachineHandler interface {
	PhysicalDisksForAPI() ([]PhysicalDisk, error)
	// ExchangeForAPI runs the slow log-mode query only when withLogMode is
	// set, and only on a machine where Exchange is installed.
	ExchangeForAPI(withLogMode bool) ExchangeResponse
}

func (s *Server) machineHandler(w http.ResponseWriter) (MachineHandler, bool) {
	h, ok := s.app.(MachineHandler)
	if !ok {
		s.writeError(w, "this agent does not serve machine queries", http.StatusNotImplemented)
		return nil, false
	}
	return h, true
}

func (s *Server) handleDisks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h, ok := s.machineHandler(w)
	if !ok {
		return
	}
	disks, err := h.PhysicalDisksForAPI()
	if err != nil {
		s.writeError(w, err.Error(), statusOf(err, http.StatusInternalServerError))
		return
	}
	if disks == nil {
		disks = []PhysicalDisk{}
	}
	s.writeJSON(w, DisksResponse{Disks: disks}, http.StatusOK)
}

func (s *Server) handleExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h, ok := s.machineHandler(w)
	if !ok {
		return
	}
	out := h.ExchangeForAPI(r.URL.Query().Get("log_mode") == "1")
	s.writeJSON(w, out, http.StatusOK)
}

// GetDisks lists the physical disks a machine backup can target.
func (c *Client) GetDisks() ([]PhysicalDisk, error) {
	var out DisksResponse
	if err := c.getJSON("/disks", &out); err != nil {
		return nil, err
	}
	if out.Disks == nil {
		out.Disks = []PhysicalDisk{}
	}
	return out.Disks, nil
}

// GetExchange returns Exchange detection, and the log mode when asked for.
func (c *Client) GetExchange(withLogMode bool) (*ExchangeResponse, error) {
	path := "/exchange"
	if withLogMode {
		path += "?log_mode=1"
	}
	var out ExchangeResponse
	if err := c.getJSON(path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
