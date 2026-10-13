package pbscommon

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// The data path (NimbusControl docs/V4-DATAPATH-PROXY.md §13.3): the client
// certificate is presented, each instance is pinned to its OWN fingerprint,
// and a full instance or one that does not answer hands the session to the
// next -- while a refusal is final.

type dpCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newDPCA(t *testing.T) *dpCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test datapath CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return &dpCA{cert: c, key: key, pool: pool}
}

func (ca *dpCA) clientCert(t *testing.T, cn string) *tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func selfSigned(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "instance"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	sum := sha256.Sum256(der)
	pairs := make([]string, len(sum))
	for i, b := range sum {
		pairs[i] = strings.ToUpper(hex.EncodeToString([]byte{b}))
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, strings.Join(pairs, ":")
}

// instance is one fake data-path instance: it demands a client certificate
// from ca, reads the upgrade, and answers with `answer` (an HTTP/1.1 status
// line plus headers/body) or, for "101", upgrades and serves HTTP/2.
type instance struct {
	url, fp string
	mu      sync.Mutex
	seen    []seenUpgrade
	tlsFail int
}

type seenUpgrade struct {
	line, auth, cn string
}

func startInstance(t *testing.T, ca *dpCA, answer string) *instance {
	t.Helper()
	cert, fp := selfSigned(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ca.pool, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	in := &instance{url: "https://" + ln.Addr().String(), fp: fp}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				tc := c.(*tls.Conn)
				if err := tc.Handshake(); err != nil {
					in.mu.Lock()
					in.tlsFail++
					in.mu.Unlock()
					c.Close()
					return
				}
				br := bufio.NewReader(tc)
				req, err := http.ReadRequest(br)
				if err != nil {
					c.Close()
					return
				}
				in.mu.Lock()
				in.seen = append(in.seen, seenUpgrade{line: req.URL.RequestURI(), auth: req.Header.Get("Authorization"),
					cn: tc.ConnectionState().PeerCertificates[0].Subject.CommonName})
				in.mu.Unlock()
				if answer != "101" {
					_, _ = io.WriteString(tc, answer)
					c.Close()
					return
				}
				_, _ = io.WriteString(tc, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: proxmox-backup-protocol-v1\r\nConnection: Upgrade\r\n\r\n")
				(&http2.Server{}).ServeConn(tc, &http2.ServeConnOpts{Handler: http.HandlerFunc(
					func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "served:"+r.URL.Path) })})
			}(c)
		}
	}()
	return in
}

func (in *instance) upgrades() []seenUpgrade {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]seenUpgrade(nil), in.seen...)
}

func jsonBody(status, body string) string {
	return fmt.Sprintf("HTTP/1.1 %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", status, len(body), body)
}

func grantClient(primary *instance, dp *DataPath) *PBSClient {
	return &PBSClient{BaseURL: primary.url, CertFingerPrint: primary.fp,
		AuthID: "nimbus@grant!gabcdefghijklmnopqrstuvwx", Secret: "grant-secret",
		Datastore: "nimbus", DataPath: dp, Manifest: BackupManifest{BackupID: "M1"}}
}

func session(t *testing.T, c *PBSClient) (string, error) {
	t.Helper()
	c.Connect(false, "host")
	defer c.Close()
	resp, err := c.Client.Get(c.BaseURL + "/probe")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

func TestAFullInstanceHandsTheSessionToTheNext(t *testing.T) {
	ca := newDPCA(t)
	full := startInstance(t, ca, jsonBody("503 Service Unavailable", `{"error":"this instance is at capacity","code":"proxy_full"}`))
	free := startInstance(t, ca, "101")
	cert := ca.clientCert(t, "nimbus-agent-17")
	c := grantClient(full, &DataPath{Cert: cert, Endpoints: []Endpoint{{full.url, full.fp}, {free.url, free.fp}}})
	body, err := session(t, c)
	if err != nil || body != "served:/probe" {
		t.Fatalf("session not opened at the second instance: %q %v", body, err)
	}
	if len(full.upgrades()) != 1 || len(free.upgrades()) != 1 {
		t.Fatalf("upgrades: full=%d free=%d", len(full.upgrades()), len(free.upgrades()))
	}
	got := free.upgrades()[0]
	if got.cn != "nimbus-agent-17" {
		t.Fatalf("the machine certificate was not presented: %+v", got)
	}
	if got.auth != "PBSAPIToken=nimbus@grant!gabcdefghijklmnopqrstuvwx:grant-secret" ||
		!strings.Contains(got.line, "store=nimbus") || strings.Contains(got.line, "ns=") {
		t.Fatalf("upgrade %+v: the grant, the virtual store, no namespace", got)
	}
}

func TestARefusalIsFinal(t *testing.T) {
	ca := newDPCA(t)
	closed := startInstance(t, ca, jsonBody("403 Forbidden", `{"error":"the operation this grant was for is over","code":"grant_closed"}`))
	other := startInstance(t, ca, "101")
	c := grantClient(closed, &DataPath{Cert: ca.clientCert(t, "nimbus-agent-17"),
		Endpoints: []Endpoint{{closed.url, closed.fp}, {other.url, other.fp}}})
	if _, err := session(t, c); err == nil {
		t.Fatal("a refused grant opened a session somewhere")
	}
	if n := len(other.upgrades()); n != 0 {
		t.Fatalf("a refused grant was presented again elsewhere (%d times): that multiplies the illegal-use rows", n)
	}
}

func TestNoMachineCertificateNoSession(t *testing.T) {
	ca := newDPCA(t)
	in := startInstance(t, ca, "101")
	c := grantClient(in, &DataPath{Endpoints: []Endpoint{{in.url, in.fp}}})
	if _, err := session(t, c); err == nil {
		t.Fatal("connected without a client certificate")
	}
	if len(in.upgrades()) != 0 {
		t.Fatal("an upgrade reached an instance without a certificate")
	}
}

func TestEachInstanceIsPinnedToItsOwnFingerprint(t *testing.T) {
	ca := newDPCA(t)
	down := "https://127.0.0.1:1" // nothing listens: the session moves on
	impostor := startInstance(t, ca, "101")
	c := &PBSClient{BaseURL: down, CertFingerPrint: impostor.fp, AuthID: "nimbus@grant!gabcdefghijklmnopqrstuvwx",
		Secret: "s", Datastore: "nimbus", Manifest: BackupManifest{BackupID: "M1"},
		DataPath: &DataPath{Cert: ca.clientCert(t, "nimbus-agent-17"), Endpoints: []Endpoint{
			{down, impostor.fp}, {impostor.url, strings.Repeat("AB:", 31) + "AB"}}}}
	if _, err := session(t, c); err == nil {
		t.Fatal("an instance answered with a certificate other than the one pinned for it")
	}
	if len(impostor.upgrades()) != 0 {
		t.Fatal("the grant was sent to an instance whose certificate did not match its pin")
	}
}

func TestListSnapshotsFallsBackOnAConnectionFailure(t *testing.T) {
	ca := newDPCA(t)
	cert, fp := selfSigned(t)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 || r.URL.Query().Get("ns") != "" {
			w.WriteHeader(400)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"backup-type":"host","backup-id":"M1","backup-time":1783486801}]}`)
	})}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ca.pool})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	up := "https://" + ln.Addr().String()
	c := &PBSClient{BaseURL: "https://127.0.0.1:1", CertFingerPrint: fp, AuthID: "nimbus@grant!gabcdefghijklmnopqrstuvwx",
		Secret: "s", Datastore: "nimbus",
		DataPath: &DataPath{Cert: ca.clientCert(t, "nimbus-agent-17"), Endpoints: []Endpoint{{up, fp}}}}
	snaps, err := c.ListSnapshots()
	if err != nil || len(snaps) != 1 || snaps[0].BackupID != "M1" {
		t.Fatalf("%v %+v", err, snaps)
	}
}
