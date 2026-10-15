package controlplane

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// The data path (NimbusControl docs/V4-DATAPATH-PROXY.md, wire in its
// docs/AGENT-API.md). When the server runs in proxy mode an enrolled machine
// reaches PBS ONLY through the server's data-path instances, and only with an
// operation grant the server minted for that one operation. There is no
// machine PBS credential: /pbs-credential answers 409 proxy_mode.

// DatapathModeProxy is the check-in's datapath.mode when the server routes
// every backup and restore through its data path.
const (
	DatapathModeDirect = "direct"
	DatapathModeProxy  = "proxy"
)

// DatapathCheckin is the check-in's `datapath` block.
type DatapathCheckin struct {
	Mode string `json:"mode"`
	// CertRenew: send a CSR to /datapath-cert now (no certificate held, or
	// inside ten days of expiry).
	CertRenew bool `json:"cert_renew"`
}

// Proxy reports whether d says the data path is in force. nil (an older
// server) is direct.
func (d *DatapathCheckin) Proxy() bool { return d != nil && d.Mode == DatapathModeProxy }

// GrantEndpoint is one data-path instance a grant may be redeemed at.
type GrantEndpoint struct {
	BaseURL     string `json:"base_url"`
	Fingerprint string `json:"fingerprint"`
}

// Grant is one operation's capability (§4). The secret is sealed to this
// machine's agent key; Datastore and Namespace are VIRTUAL ("nimbus", "") and
// say nothing about where the backups really are.
type Grant struct {
	AuthID          string          `json:"auth_id"`
	SecretSealedB64 string          `json:"secret_sealed"`
	SealedToKeyID   string          `json:"sealed_to_key_id"`
	BaseURL         string          `json:"base_url"`
	Fingerprint     string          `json:"fingerprint"`
	Fallbacks       []GrantEndpoint `json:"fallbacks"`
	Datastore       string          `json:"datastore"`
	Namespace       string          `json:"namespace"`
	Kind            string          `json:"kind"` // backup | restore
	RedeemBy        string          `json:"redeem_by"`
	// BackupIDs are the PBS backup-ids a BACKUP grant's run may write, and
	// the only ones the data path accepts (docs/V4-PBS-OPACITY.md §9.1 on
	// the server). The server names every backup group; this machine never
	// derives one from its hostname or a path in proxy mode.
	BackupIDs []GrantBackupID `json:"backup_ids,omitempty"`
}

// GrantBackupID pairs a source with the backup-id the server assigned it:
// Source "machine" for the image backup, otherwise a directory exactly as
// the job names it.
type GrantBackupID struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}

// MachineSource is the Source of the machine (image) backup's id.
const MachineSource = "machine"

// The shape the server allocates: g + 16, and -<n> for a directory source.
var grantBackupIDRe = regexp.MustCompile(`^g[a-z0-9]{16}(-[1-9][0-9]*)?$`)

func (g *Grant) backupID(source string) (string, bool) {
	if g == nil {
		return "", false
	}
	for _, b := range g.BackupIDs {
		if b.Source == source && grantBackupIDRe.MatchString(b.ID) {
			return b.ID, true
		}
	}
	return "", false
}

// MachineBackupID is the id an image backup writes under.
func (g *Grant) MachineBackupID() (string, error) {
	if id, ok := g.backupID(MachineSource); ok {
		return id, nil
	}
	return "", errors.New("the control server assigned this machine no backup id; the backup was not started")
}

// SourceBackupIDs maps every directory to the id the server assigned it.
// One missing is an error naming it: a directory without an id is not
// backed up, never backed up under a name made up here.
func (g *Grant) SourceBackupIDs(dirs []string) (map[string]string, error) {
	out := make(map[string]string, len(dirs))
	for _, d := range dirs {
		id, ok := g.backupID(d)
		if !ok {
			return nil, fmt.Errorf("the control server assigned no backup id for %q; the backup was not started", d)
		}
		out[d] = id
	}
	return out, nil
}

// Validate refuses a grant the engines could not use, before anything is
// opened or connected.
func (g *Grant) Validate() error {
	if g == nil {
		return errors.New("controlplane: no grant")
	}
	if !strings.HasPrefix(g.AuthID, "nimbus@grant!") || g.SecretSealedB64 == "" ||
		!strings.HasPrefix(g.BaseURL, "https://") || g.Datastore == "" {
		return fmt.Errorf("controlplane: incomplete grant (auth_id=%q base_url=%q datastore=%q)", g.AuthID, g.BaseURL, g.Datastore)
	}
	if _, err := base64.StdEncoding.DecodeString(g.SecretSealedB64); err != nil {
		return fmt.Errorf("controlplane: grant secret is not base64: %w", err)
	}
	return nil
}

// Endpoints is the grant's instance followed by its fallbacks, in the order
// they are tried.
func (g *Grant) Endpoints() []GrantEndpoint {
	out := []GrantEndpoint{{BaseURL: g.BaseURL, Fingerprint: g.Fingerprint}}
	for _, f := range g.Fallbacks {
		if f.BaseURL != "" && f.BaseURL != g.BaseURL {
			out = append(out, f)
		}
	}
	return out
}

// RefusedError is the server declining a request with a machine-readable
// code (org_suspended, unmanaged_restricted, datapath_unavailable,
// restore_disabled, ...). Message is the sentence for a person.
type RefusedError struct {
	Status  int
	Code    string
	Message string
}

func (e *RefusedError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s (%s)", e.Message, e.Code)
	}
	return e.Message
}

// asRefused turns a 4xx with a body into a RefusedError; anything else (a
// transport failure, a 5xx after retries) is returned as it is.
func asRefused(err error) error {
	var he *httpError
	if asHTTPError(err, &he) && he.status >= 400 && he.status < 500 && he.status != 429 {
		return &RefusedError{Status: he.status, Code: he.code, Message: he.msg}
	}
	return err
}

// BackupRequest is POST /api/agent/v1/backup-request: the handshake for a
// backup this machine starts itself (V4-RUN-AUDIT 1.3a), and in proxy mode
// the only way such a backup gets a grant.
type BackupRequest struct {
	JobName string `json:"job_name"`
	JobID   int64  `json:"job_id,omitempty"`
	Managed bool   `json:"managed"`
	// Sources: a directory backup's directories, exactly as the job names
	// them, so the grant carries an id for each (max 64). Omit for an image
	// backup.
	Sources []string `json:"sources,omitempty"`
}

// BackupRequestResponse carries the request id, and in proxy mode the grant.
type BackupRequestResponse struct {
	RequestID string `json:"request_id"`
	Grant     *Grant `json:"grant,omitempty"`
}

// RequestBackup asks the server whether this backup may run. A refusal is a
// *RefusedError; the run must not start.
func (c *Client) RequestBackup(req BackupRequest) (*BackupRequestResponse, error) {
	var out BackupRequestResponse
	if err := c.post("/api/agent/v1/backup-request", req, &out, true); err != nil {
		return nil, asRefused(err)
	}
	if out.RequestID == "" {
		return nil, errors.New("controlplane: backup-request returned no request_id")
	}
	if out.Grant != nil {
		if err := out.Grant.Validate(); err != nil {
			return nil, err
		}
	}
	return &out, nil
}

// RestoreRequest is POST /api/agent/v1/restore-request. With no snapshot the
// grant is a BROWSE grant over this machine's own snapshots.
type RestoreRequest struct {
	RestoreUUID string `json:"restore_uuid"`
	BackupType  string `json:"backup_type,omitempty"`
	BackupID    string `json:"backup_id,omitempty"`
	BackupTime  int64  `json:"backup_time,omitempty"`
}

// RequestRestore asks for a restore grant. Refusals (restore_disabled,
// not_own_snapshot, direct_mode, ...) are *RefusedError.
func (c *Client) RequestRestore(req RestoreRequest) (*Grant, error) {
	var out struct {
		Grant *Grant `json:"grant"`
	}
	if err := c.post("/api/agent/v1/restore-request", req, &out, true); err != nil {
		return nil, asRefused(err)
	}
	if err := out.Grant.Validate(); err != nil {
		return nil, err
	}
	return out.Grant, nil
}

// DatapathCertResponse is the signed client certificate and its CA.
type DatapathCertResponse struct {
	Certificate string `json:"certificate"`
	CA          string `json:"ca"`
	ExpiresAt   string `json:"expires_at"`
}

// RequestDatapathCert sends this machine's CSR (subject exactly
// CN=nimbus-agent-<id>) and returns the signed certificate.
func (c *Client) RequestDatapathCert(csrPEM string) (*DatapathCertResponse, error) {
	var out DatapathCertResponse
	if err := c.post("/api/agent/v1/datapath-cert", map[string]string{"csr": csrPEM}, &out, true); err != nil {
		return nil, asRefused(err)
	}
	if !strings.Contains(out.Certificate, "BEGIN CERTIFICATE") {
		return nil, errors.New("controlplane: datapath-cert returned no certificate")
	}
	return &out, nil
}
