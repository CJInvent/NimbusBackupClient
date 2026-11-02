//go:build windows

package api

// The named pipe on real Windows (docs/V4-LOCAL-API.md §10, phase P1's
// tests). These run in CI's Windows Smoke job, which runs this package's
// whole suite on windows-latest; on Linux they do not compile, and the pipe
// has nothing to stand on there.
//
// WHAT THE TESTS STAND IN FOR, AND WHY. In production the server is SYSTEM
// and the client is an interactive user. A test is one process, so it is both
// ends. The pipes below therefore carry the production descriptor with THIS
// process's user substituted for SYSTEM (as owner and as the server's ACE);
// testSDDL derives it from pipeSDDL rather than restating it, so the shape
// under test is the shipped one. A pipe owned by SYSTEM cannot be made here:
// TestThePipeSecurityDescriptorIsTheSpecs checks that descriptor directly.
//
// Every refusal below is paired with the same setup succeeding, so a refusal
// cannot be something else failing.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var pipeSeq atomic.Int64

// testPipeName is a name no other test, run or service uses.
func testPipeName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(`\\.\pipe\NimbusBackupTest-%d-%d`, os.Getpid(), pipeSeq.Add(1))
}

// selfSID is this process's user.
func selfSID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("this process's user: %v", err)
	}
	return u.User.Sid.String()
}

// testSDDL is pipeSDDL with this process's user where SYSTEM is: the owner,
// and the server's full-control entry. INTERACTIVE's restricted entry and
// NETWORK's denial are untouched.
func testSDDL(t *testing.T) string {
	t.Helper()
	sid := selfSID(t)
	s := strings.Replace(pipeSDDL, "O:SY", "O:"+sid, 1)
	s = strings.Replace(s, "(A;;GA;;;SY)", "(A;;GA;;;"+sid+")", 1)
	if strings.Count(s, sid) != 2 || strings.Contains(s, ";SY)") || strings.HasPrefix(s, "O:SY") {
		t.Fatalf("testSDDL no longer matches pipeSDDL's shape (%q -> %q); update the substitution", pipeSDDL, s)
	}
	return s
}

// selfCheck accepts a pipe owned by this process's user and served by this
// process: the production check's shape with this test's identities.
func selfCheck(t *testing.T) pipeCheck {
	return pipeCheck{
		owner:      selfSID(t),
		servicePID: func() (uint32, error) { return uint32(os.Getpid()), nil },
	}
}

func testPipeClient(name string, check pipeCheck) *Client {
	return &Client{
		baseURL: "http://nimbus-service",
		dial: func(timeout time.Duration) (net.Conn, error) {
			return dialPipe(name, check, timeout)
		},
	}
}

// countingListener counts every byte the server reads, so "the client sent
// nothing" is a measurement rather than an assumption.
type countingListener struct {
	net.Listener
	read *atomic.Int64
}

type countingConn struct {
	*pipeConn
	read *atomic.Int64
}

func (c countingConn) Read(b []byte) (int, error) {
	n, err := c.pipeConn.Read(b)
	c.read.Add(int64(n))
	return n, err
}

func (l countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	pc, ok := c.(*pipeConn)
	if !ok {
		return nil, fmt.Errorf("the pipe listener returned %T, not *pipeConn", c)
	}
	return countingConn{pipeConn: pc, read: l.read}, nil
}

// servePipe serves the real Server on a pipe with the test descriptor, and
// returns how many bytes the server has read.
func servePipe(t *testing.T, name string) *atomic.Int64 {
	t.Helper()
	ln, err := listenPipe(name, testSDDL(t), pipeMode)
	if err != nil {
		t.Fatalf("listen on %s: %v", name, err)
	}
	read := new(atomic.Int64)
	s := NewServer(newStub())
	go func() { _ = s.Serve(countingListener{Listener: ln, read: read}) }()
	t.Cleanup(func() { _ = ln.Close() })
	return read
}

// ownIdentity is what the service must see for an unimpersonating client of
// this process: describeToken over the process token, duplicated as an
// impersonation token as the pipe's client token is.
func ownIdentity(t *testing.T) *Caller {
	t.Helper()
	var proc windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &proc); err != nil {
		t.Fatalf("open the process token: %v", err)
	}
	defer func() { _ = proc.Close() }()
	var imp windows.Token
	if err := windows.DuplicateTokenEx(proc, windows.TOKEN_QUERY, nil, windows.SecurityIdentification, windows.TokenImpersonation, &imp); err != nil {
		t.Fatalf("duplicate the process token: %v", err)
	}
	defer func() { _ = imp.Close() }()
	who, err := describeToken(imp)
	if err != nil {
		t.Fatalf("describe this process's token: %v", err)
	}
	return who
}

func sameIdentity(got, want *Caller) error {
	if got.UserSID != want.UserSID || got.SessionID != want.SessionID || got.Elevated != want.Elevated || got.Admin != want.Admin {
		return fmt.Errorf("the service saw user %s session %d elevated=%t admin=%t; the token is user %s session %d elevated=%t admin=%t",
			got.UserSID, got.SessionID, got.Elevated, got.Admin, want.UserSID, want.SessionID, want.Elevated, want.Admin)
	}
	return nil
}

func TestThePipeTellsTheServiceWhoIsCalling(t *testing.T) {
	name := testPipeName(t)
	servePipe(t, name)

	want := ownIdentity(t)
	if want.UserSID == "" {
		t.Fatal("this process's own identity came back empty; nothing below would mean anything")
	}
	got, err := testPipeClient(name, selfCheck(t)).Caller()
	if err != nil {
		t.Fatalf("GET /caller over the pipe: %v", err)
	}
	if err := sameIdentity(got, want); err != nil {
		t.Fatal(err)
	}
	t.Logf("the runner's own token, as the service saw it: %s (%s) session %d elevated=%t admin=%t",
		got.Account, got.UserSID, got.SessionID, got.Elevated, got.Admin)
}

// createRestrictedToken is advapi32's, which x/sys does not wrap.
var procCreateRestrictedToken = modadvapi32.NewProc("CreateRestrictedToken")

const (
	disableMaxPrivilege = 0x1
	luaToken            = 0x4
)

// nonElevatedToken is this process's token as a UAC-filtered standard user
// would hold it: Administrators deny-only, every privilege but
// SeChangeNotify removed. Windows judges elevation from exactly those two
// things, so this is not elevated whatever the runner's own token is. As an
// impersonation token, ready for a thread.
func nonElevatedToken(t *testing.T) windows.Token {
	t.Helper()
	var proc windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_IMPERSONATE, &proc); err != nil {
		t.Fatalf("open the process token: %v", err)
	}
	defer func() { _ = proc.Close() }()
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	disable := windows.SIDAndAttributes{Sid: admins}
	var restricted windows.Token
	r, _, e := procCreateRestrictedToken.Call(uintptr(proc), disableMaxPrivilege|luaToken,
		1, uintptr(unsafe.Pointer(&disable)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted)))
	if r == 0 {
		t.Fatalf("CreateRestrictedToken: %v", e)
	}
	defer func() { _ = restricted.Close() }()
	var imp windows.Token
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE|windows.TOKEN_DUPLICATE, nil,
		windows.SecurityImpersonation, windows.TokenImpersonation, &imp); err != nil {
		t.Fatalf("duplicate the restricted token: %v", err)
	}
	t.Cleanup(func() { _ = imp.Close() })
	return imp
}

// asToken runs f on a thread impersonating tok, and proves the impersonation
// took (Windows silently drops to identification level when it will not let
// a thread impersonate, and then the test would be measuring something else).
func asToken(t *testing.T, tok windows.Token, f func()) {
	t.Helper()
	failed := make(chan string, 1)
	go func() {
		// Locked, and only unlocked after a successful revert: a thread
		// still wearing the test's token must die with this goroutine.
		runtime.LockOSThread()
		defer close(failed)
		if err := windows.SetThreadToken(nil, tok); err != nil {
			failed <- fmt.Sprintf("SetThreadToken: %v", err)
			return
		}
		var cur windows.Token
		if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &cur); err != nil {
			_ = windows.RevertToSelf()
			failed <- fmt.Sprintf("the thread's token after SetThreadToken: %v", err)
			return
		}
		var level, n uint32
		lerr := windows.GetTokenInformation(cur, windows.TokenImpersonationLevel, (*byte)(unsafe.Pointer(&level)), 4, &n)
		_ = cur.Close()
		if lerr != nil || level != windows.SecurityImpersonation {
			_ = windows.RevertToSelf()
			failed <- fmt.Sprintf("the thread is not impersonating (level %d, %v)", level, lerr)
			return
		}
		f()
		if windows.RevertToSelf() == nil {
			runtime.UnlockOSThread()
		}
	}()
	if msg, ok := <-failed; ok {
		t.Fatal(msg)
	}
}

func TestANonElevatedClientIsSeenAsNotElevated(t *testing.T) {
	name := testPipeName(t)
	servePipe(t, name)

	tok := nonElevatedToken(t)
	// Precondition: the token really is a standard user's, read the way the
	// service reads it. If this fails the test cannot say anything.
	pre, err := describeToken(tok)
	if err != nil {
		t.Fatalf("describe the non-elevated token: %v", err)
	}
	if pre.Elevated || pre.Admin {
		t.Fatalf("the test could not make a non-elevated token (elevated=%t admin=%t)", pre.Elevated, pre.Admin)
	}

	var got *Caller
	var callErr error
	asToken(t, tok, func() { got, callErr = testPipeClient(name, selfCheck(t)).Caller() })
	if callErr != nil {
		t.Fatalf("GET /caller as a standard user: %v", callErr)
	}
	if got.Elevated || got.Admin {
		t.Errorf("a standard user's client was seen as elevated=%t admin=%t; want false and false", got.Elevated, got.Admin)
	}
	if got.UserSID != selfSID(t) {
		t.Errorf("the service saw user %s, want %s", got.UserSID, selfSID(t))
	}

	// The contrast, where the runner gives one: its own token through the
	// same pipe. On a runner whose token is elevated, this is what proves the
	// false above came from the token and not from a constant.
	own, err := testPipeClient(name, selfCheck(t)).Caller()
	if err != nil {
		t.Fatalf("GET /caller as the runner: %v", err)
	}
	if err := sameIdentity(own, ownIdentity(t)); err != nil {
		t.Fatal(err)
	}
	t.Logf("runner token: elevated=%t admin=%t; standard-user token: elevated=%t admin=%t", own.Elevated, own.Admin, got.Elevated, got.Admin)
}

func TestAProcessHoldingThePipeNameStopsTheService(t *testing.T) {
	name := testPipeName(t)
	// The squatter: any process creating the name first, with whatever
	// descriptor it likes.
	squat, err := createPipeInstance(name, nil, true, pipeMode)
	if err != nil {
		t.Fatalf("create the squatting pipe: %v", err)
	}
	ln, err := listenPipe(name, testSDDL(t), pipeMode)
	if err == nil {
		_ = ln.Close()
		_ = windows.CloseHandle(squat)
		t.Fatal("the service listened on a pipe name another process already held")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Errorf("listening behind a squatter failed with %v; want ERROR_ACCESS_DENIED, which is FILE_FLAG_FIRST_PIPE_INSTANCE's refusal", err)
	}

	// The same name, once the squatter lets go, serves: the refusal above was
	// the squatter.
	_ = windows.CloseHandle(squat)
	ln, err = listenPipe(name, testSDDL(t), pipeMode)
	if err != nil {
		t.Fatalf("listening on a free name failed: %v", err)
	}
	_ = ln.Close()
}

// remoteName is name as a client on another machine would open it: over SMB,
// to this machine.
func remoteName(name string) string {
	return `\\localhost\pipe\` + strings.TrimPrefix(name, `\\.\pipe\`)
}

func openPipe(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, clientAccess, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return err
	}
	return windows.CloseHandle(h)
}

func TestThePipeRefusesRemoteClients(t *testing.T) {
	sid := selfSID(t)
	permissive := "D:(A;;GA;;;" + sid + ")"
	local := pipeMode &^ uint32(windows.PIPE_REJECT_REMOTE_CLIENTS)

	instance := func(sddl string, mode uint32) string {
		t.Helper()
		name := testPipeName(t)
		sa, err := securityAttributes(sddl)
		if err != nil {
			t.Fatal(err)
		}
		h, err := createPipeInstance(name, sa, true, mode)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		t.Cleanup(func() { _ = windows.CloseHandle(h) })
		return name
	}

	// Control: a pipe with neither defense IS reachable this way. Without
	// it, every refusal below could be SMB being unavailable on the runner.
	control := instance(permissive, local)
	if err := openPipe(remoteName(control)); err != nil {
		t.Fatalf("the runner cannot reach its own pipe over SMB (%v), so no refusal below would prove anything", err)
	}

	// Each defense alone refuses.
	if err := openPipe(remoteName(instance(permissive, pipeMode))); err == nil {
		t.Error("PIPE_REJECT_REMOTE_CLIENTS alone did not refuse a remote client")
	}
	if err := openPipe(remoteName(instance(testSDDL(t), local))); err == nil {
		t.Error("the descriptor's NETWORK denial alone did not refuse a remote client")
	}

	// The listener as the service runs it refuses a remote client and admits
	// a local one.
	name := testPipeName(t)
	servePipe(t, name)
	if err := openPipe(remoteName(name)); err == nil {
		t.Error("the service's pipe admitted a remote client")
	}
	if _, err := testPipeClient(name, selfCheck(t)).Caller(); err != nil {
		t.Errorf("the service's pipe refused a local client: %v", err)
	}
}

func TestTheConsoleRefusesAServerThatIsNotTheService(t *testing.T) {
	name := testPipeName(t)
	read := servePipe(t, name)

	// The production check against a server that is not SYSTEM: refused by
	// the owner check, before a byte is written.
	_, err := testPipeClient(name, serviceCheck()).Caller()
	if !errors.Is(err, ErrServerNotTrusted) {
		t.Fatalf("the console talked to a pipe this user owns: err = %v, want ErrServerNotTrusted", err)
	}
	if want := "the pipe belongs to " + selfSID(t) + ", not " + systemSID; !strings.Contains(err.Error(), want) {
		t.Errorf("refused, but not by the owner check: %v (want %q)", err, want)
	}

	// Owner right, process wrong: refused by the process check.
	wrongPID := selfCheck(t)
	wrongPID.servicePID = func() (uint32, error) { return 4, nil } // the System process
	_, err = testPipeClient(name, wrongPID).Caller()
	if !errors.Is(err, ErrServerNotTrusted) || !strings.Contains(err.Error(), "is served by process") {
		t.Errorf("a pipe served by another process than the service's: err = %v, want the process check's refusal", err)
	}

	// Give a refused client's connection time to reach the server; it must
	// have carried nothing.
	time.Sleep(200 * time.Millisecond)
	if n := read.Load(); n != 0 {
		t.Errorf("the server read %d bytes from clients that refused it; the console must send nothing before the check", n)
	}

	// The same server, with a check it satisfies, is talked to: so the
	// refusals were the checks, and the byte count can count.
	if _, err := testPipeClient(name, selfCheck(t)).Caller(); err != nil {
		t.Fatalf("the console refused a server that passes its check: %v", err)
	}
	if read.Load() == 0 {
		t.Error("the server read nothing from an accepted client; the zero above proved nothing")
	}
}

func TestABodyClaimingAnIdentityGetsTheTokens(t *testing.T) {
	name := testPipeName(t)
	servePipe(t, name)

	claim := Caller{UserSID: systemSID, Account: `NT AUTHORITY\SYSTEM`, SessionID: 0, Elevated: true, Admin: true}
	want := ownIdentity(t)
	if want.UserSID == claim.UserSID {
		t.Fatal("this test runs as SYSTEM; the claim would be the truth")
	}
	body, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	c := testPipeClient(name, selfCheck(t))
	resp, err := c.send(http.MethodGet, "/caller?user_sid="+systemSID+"&elevated=true", body, RequestTimeout)
	if err != nil {
		t.Fatalf("GET /caller with a claim: %v", err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /caller with a claim: HTTP %d %s (%v)", resp.StatusCode, raw, err)
	}
	var got Caller
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if err := sameIdentity(&got, want); err != nil {
		t.Fatalf("a request claiming to be SYSTEM: %v", err)
	}
}

func TestThePipeSecurityDescriptorIsTheSpecs(t *testing.T) {
	// INTERACTIVE's mask is read and write without FILE_CREATE_PIPE_INSTANCE
	// (FILE_APPEND_DATA's value on a pipe), derived here, not trusted.
	const interactive = (windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE) &^ windows.FILE_APPEND_DATA
	if !strings.Contains(pipeSDDL, fmt.Sprintf("(A;;%#x;;;IU)", interactive)) {
		t.Errorf("pipeSDDL does not grant INTERACTIVE %#x: %s", interactive, pipeSDDL)
	}
	// The console asks only for what INTERACTIVE is granted.
	if extra := clientAccess &^ interactive; extra != 0 {
		t.Errorf("the console asks for %#x, which INTERACTIVE is not granted", extra)
	}

	sd, err := windows.SecurityDescriptorFromString(pipeSDDL)
	if err != nil {
		t.Fatalf("pipeSDDL does not parse: %v", err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner.String() != systemSID {
		t.Errorf("the pipe's owner is %v (%v), want SYSTEM", owner, err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("the pipe's DACL is not protected (control %#x, %v)", control, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("the pipe's DACL: %v", err)
	}
	type entry struct {
		typ  uint8
		sid  windows.WELL_KNOWN_SID_TYPE
		mask uint32
	}
	const all = windows.GENERIC_ALL
	want := []entry{
		{windows.ACCESS_DENIED_ACE_TYPE, windows.WinNetworkSid, all},
		{windows.ACCESS_ALLOWED_ACE_TYPE, windows.WinLocalSystemSid, all},
		{windows.ACCESS_ALLOWED_ACE_TYPE, windows.WinBuiltinAdministratorsSid, all},
		{windows.ACCESS_ALLOWED_ACE_TYPE, windows.WinInteractiveSid, interactive},
	}
	if int(dacl.AceCount) != len(want) {
		t.Fatalf("the pipe's DACL has %d entries, want %d", dacl.AceCount, len(want))
	}
	for i, w := range want {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Header.AceType != w.typ || !sid.IsWellKnown(w.sid) || uint32(ace.Mask) != w.mask {
			t.Errorf("entry %d is type %d %s mask %#x; want type %d well-known SID %d mask %#x",
				i, ace.Header.AceType, sid, uint32(ace.Mask), w.typ, w.sid, w.mask)
		}
	}
	// NETWORK's denial comes first: a deny after the allows would be read
	// after an allow had already matched.
}
