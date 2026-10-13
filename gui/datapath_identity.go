package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"controlplane"
)

// This machine's data-path identity (NimbusControl docs/V4-DATAPATH-PROXY.md
// §7, §13.2): an ECDSA P-256 key and the client certificate the control
// server's CA signed for it, CN=nimbus-agent-<id>. The data-path instances
// refuse every TLS handshake without one, so nothing unauthenticated reaches
// them at all.
//
// The private key is sealed under the same DEK and protector chain as the
// agent key (agentkey_store.go): one answer to "what protects this machine's
// secrets". A NEW key on every renewal; the old one is simply replaced.

const datapathIdentityFile = "datapath-identity.json"

// datapathRenewCooldown keeps renewals well inside the server's 6 an hour.
const datapathRenewCooldown = 15 * time.Minute

type storedDatapathIdentity struct {
	AgentID   int64  `json:"agent_id"`
	Protector string `json:"protector"`
	KeySealed string `json:"key_sealed"` // PKCS#8, sealed under the DEK
	CertPEM   string `json:"cert_pem"`
	CAPEM     string `json:"ca_pem"`
	ExpiresAt string `json:"expires_at"`
}

var (
	dpIdentityMu    sync.Mutex
	dpRenewAttempt  time.Time
	errNoDPIdentity = errors.New("no data path certificate stored on this machine")
)

// datapathCertSigner is the one call a renewal makes, so a test can stand in
// for the control server.
type datapathCertSigner interface {
	RequestDatapathCert(csrPEM string) (*controlplane.DatapathCertResponse, error)
}

func datapathIdentityPath() (string, error) {
	dir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, datapathIdentityFile), nil
}

func readDatapathIdentity() (*storedDatapathIdentity, error) {
	path, err := datapathIdentityPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path derives from getConfigDir, not user input
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNoDPIdentity
	}
	if err != nil {
		return nil, fmt.Errorf("reading this machine's data path certificate failed: %w", err)
	}
	var rec storedDatapathIdentity
	if err := json.Unmarshal(data, &rec); err != nil || rec.KeySealed == "" || rec.CertPEM == "" {
		return nil, errors.New("this machine's data path certificate record is corrupt")
	}
	return &rec, nil
}

// datapathCertNeeded says whether to ask for a certificate now, and why.
func datapathCertNeeded(cfg *Config, serverAsked bool) (bool, string) {
	dpIdentityMu.Lock()
	recent := !dpRenewAttempt.IsZero() && time.Since(dpRenewAttempt) < datapathRenewCooldown
	dpIdentityMu.Unlock()
	if recent {
		return false, ""
	}
	if serverAsked {
		return true, "the control server asked for a renewal"
	}
	rec, err := readDatapathIdentity()
	switch {
	case err != nil:
		return true, "none is held"
	case rec.AgentID != cfg.ControlAgentID:
		return true, "the one held was issued to another enrollment"
	}
	if exp, perr := time.Parse(time.RFC3339, rec.ExpiresAt); perr != nil || time.Until(exp) < 24*time.Hour {
		return true, "the one held is about to expire"
	}
	return false, ""
}

// renewDatapathIdentity makes a new key, has the control server sign it, and
// stores the pair -- read back and checked before it counts.
func renewDatapathIdentity(signer datapathCertSigner, agentID int64, why string) error {
	dpIdentityMu.Lock()
	dpRenewAttempt = time.Now()
	dpIdentityMu.Unlock()
	if agentID <= 0 {
		return errors.New("this machine is not enrolled")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating the data path key failed: %w", err)
	}
	cn := "nimbus-agent-" + strconv.FormatInt(agentID, 10)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		return fmt.Errorf("building the certificate request failed: %w", err)
	}
	resp, err := signer.RequestDatapathCert(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})))
	if err != nil {
		return fmt.Errorf("the control server did not sign this machine's certificate: %w", err)
	}

	// What came back must be OUR key under OUR name: anything else would be
	// a certificate this machine cannot use, or one naming another machine.
	block, _ := pem.Decode([]byte(resp.Certificate))
	if block == nil {
		return errors.New("the signed certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("the signed certificate does not parse: %w", err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return errors.New("the signed certificate does not carry this machine's key")
	}
	if cert.Subject.CommonName != cn {
		return fmt.Errorf("the signed certificate names %q, not %q", cert.Subject.CommonName, cn)
	}

	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	dek, protector, err := dekSource()
	if err != nil {
		return fmt.Errorf("refusing to store a data path key without a master key: %w", err)
	}
	sealed, err := sealWithDEK(dek, pkcs8)
	if err != nil {
		return err
	}
	rec := storedDatapathIdentity{AgentID: agentID, Protector: protector,
		KeySealed: base64.StdEncoding.EncodeToString(sealed), CertPEM: resp.Certificate, CAPEM: resp.CA,
		ExpiresAt: cert.NotAfter.UTC().Format(time.RFC3339)}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path, err := datapathIdentityPath()
	if err != nil {
		return err
	}
	if err := atomicWriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing the data path certificate failed: %w", err)
	}
	if err := restrictToServiceOnly(path); err != nil {
		writeWarnLog(fmt.Sprintf("[DataPath] WARNING: could not restrict the certificate file's ACL: %v", err))
	}
	got, err := loadDatapathCert()
	if err != nil {
		return fmt.Errorf("the stored data path certificate did not survive being written: %w", err)
	}
	if !bytes.Equal(got.Certificate[0], cert.Raw) {
		return errors.New("the stored data path certificate came back different")
	}
	writeInfoLog(fmt.Sprintf("[DataPath] stored a new machine certificate (%s; serial %s, expires %s) under the %s protector",
		why, cert.SerialNumber.Text(16), rec.ExpiresAt, protector))
	return nil
}

// loadDatapathCert opens the stored pair as the certificate TLS presents.
func loadDatapathCert() (*tls.Certificate, error) {
	rec, err := readDatapathIdentity()
	if err != nil {
		return nil, err
	}
	dek, _, err := dekSource()
	if err != nil {
		return nil, fmt.Errorf("cannot open the data path key: %w", err)
	}
	sealed, err := base64.StdEncoding.DecodeString(rec.KeySealed)
	if err != nil {
		return nil, errors.New("the stored data path key is corrupt")
	}
	pkcs8, err := openWithDEK(dek, sealed)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	pair, err := tls.X509KeyPair([]byte(rec.CertPEM), keyPEM)
	if err != nil {
		return nil, fmt.Errorf("the stored data path certificate and key do not match: %w", err)
	}
	return &pair, nil
}
