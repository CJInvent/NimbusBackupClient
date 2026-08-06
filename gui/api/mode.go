package api

// ExecutionMode represents how the application is running
type ExecutionMode int

const (
	// ModeService — the service answered, and the GUI talks to it. The only
	// mode in which anything can be started.
	ModeService ExecutionMode = iota
	// ModeServiceUnavailable — the service did not answer. The GUI shows
	// what it last knew and refuses to start anything.
	//
	// This was called ModeStandalone, and the name was load-bearing in the
	// wrong direction: it read as a capability ("run standalone") when the
	// condition it actually describes is a failure ("no service answered").
	// Code branched on it to run a backup engine, a scheduler and a
	// control-plane loop inside the GUI, which is what
	// docs/V4-PIPELINE.md §3.1 removes. Nothing branches on it now except
	// to refuse.
	ModeServiceUnavailable
	// ModeInProcess — "I AM the service". Set by the service process itself,
	// which executes backups in-process rather than asking anyone.
	//
	// The old enum used ModeStandalone for both this and the case above,
	// which is why the same constant could mean "the service runs the work"
	// and "no service is running" (V4-CLIENT-CONFIG.md §5.2.1 flagged the
	// collision). Two facts, two names.
	ModeInProcess
)

// ModeDetector handles execution mode detection
type ModeDetector struct {
	client *Client
}

// NewModeDetector creates a new mode detector. tokenPath is the shared local-API
// token file so the detector's /status probe is authenticated (H-01).
func NewModeDetector(tokenPath string) *ModeDetector {
	return &ModeDetector{
		client: NewClient(tokenPath),
	}
}

// DetectMode checks whether the service is reachable.
func (d *ModeDetector) DetectMode() ExecutionMode {
	if d.client.IsServiceAvailable() {
		return ModeService
	}
	return ModeServiceUnavailable
}

// GetModeName returns a human-readable mode name
func (m ExecutionMode) String() string {
	switch m {
	case ModeService:
		return "Service Mode"
	case ModeServiceUnavailable:
		return "Service Unavailable"
	case ModeInProcess:
		return "In-Process (service)"
	default:
		return "Unknown Mode"
	}
}
