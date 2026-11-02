//go:build windows

package api

// The local API's named pipe (docs/V4-LOCAL-API.md §3, phase P1): the
// service's listener, the identity it reads from each client, and the
// console's dial with its check that the server is the service.
//
// Everything here is x/sys/windows and the standard library; no pipe
// library. The handles are opened for overlapped I/O and handed to os.NewFile,
// which (since Go 1.25) puts them on the runtime's I/O completion port: reads
// and writes do not hold an OS thread, and deadlines work, which is what lets
// net/http's timeouts and the client's bounded exchange apply to a pipe.

import (
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// pipeSDDL is the pipe's security descriptor (§3):
//
//	O:SY                  owned by SYSTEM, set explicitly: a process that is
//	                      not SYSTEM cannot name SYSTEM as owner, so the
//	                      console's owner check proves who created the pipe
//	D:P                   a protected DACL
//	(D;;GA;;;NU)          NETWORK logons denied outright, before any allow
//	(A;;GA;;;SY)          SYSTEM, full control
//	(A;;GA;;;BA)          Administrators, full control
//	(A;;0x12019b;;;IU)    INTERACTIVE: read and write, WITHOUT
//	                      FILE_CREATE_PIPE_INSTANCE (0x4), so no logged-on
//	                      user can add a server instance of their own
//
// 0x12019b is (FILE_GENERIC_READ | FILE_GENERIC_WRITE) &^ FILE_APPEND_DATA,
// FILE_APPEND_DATA being FILE_CREATE_PIPE_INSTANCE's value on a pipe.
// TestThePipeSecurityDescriptorIsTheSpecs derives it rather than trusting the
// literal.
const pipeSDDL = "O:SYD:P(D;;GA;;;NU)(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x12019b;;;IU)"

// systemSID is LocalSystem, the account the service runs as.
const systemSID = "S-1-5-18"

// pipeMode is every instance's mode: a byte stream (HTTP is one), and
// PIPE_REJECT_REMOTE_CLIENTS, so a connection arriving over SMB is refused
// whatever the DACL says.
const pipeMode uint32 = windows.PIPE_TYPE_BYTE | windows.PIPE_READMODE_BYTE | windows.PIPE_WAIT | windows.PIPE_REJECT_REMOTE_CLIENTS

// pipeBuffer is each direction's buffer. A request or an answer larger than
// this simply takes more than one transfer.
const pipeBuffer = 64 << 10

// clientAccess is what the console asks for: read, write, and READ_CONTROL
// (inside FILE_GENERIC_READ) so it can read the pipe's owner. Not
// GENERIC_WRITE: that includes FILE_CREATE_PIPE_INSTANCE, which the DACL
// withholds from INTERACTIVE, and the open would be refused.
const clientAccess = windows.FILE_GENERIC_READ | windows.FILE_WRITE_DATA

// clientFlags open the pipe so that the server can IDENTIFY the console and
// not act as it (SECURITY_IDENTIFICATION): until the console has checked the
// server, it must not hand an impostor a token it could use.
const clientFlags = windows.FILE_FLAG_OVERLAPPED | windows.SECURITY_SQOS_PRESENT | windows.SECURITY_IDENTIFICATION

var (
	modadvapi32 = windows.NewLazySystemDLL("advapi32.dll")
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procImpersonateNamedPipeClient = modadvapi32.NewProc("ImpersonateNamedPipeClient")
	procWaitNamedPipeW             = modkernel32.NewProc("WaitNamedPipeW")
)

func impersonateNamedPipeClient(h windows.Handle) error {
	r, _, e := procImpersonateNamedPipeClient.Call(uintptr(h))
	if r == 0 {
		return e
	}
	return nil
}

func waitNamedPipe(name *uint16, timeout time.Duration) {
	ms := timeout.Milliseconds()
	switch {
	case ms < 1:
		ms = 1
	case ms > math.MaxInt32:
		ms = math.MaxInt32
	}
	_, _, _ = procWaitNamedPipeW.Call(uintptr(unsafe.Pointer(name)), uintptr(ms))
}

// --- the service's side ------------------------------------------------------

// ListenPipe creates the local API's pipe and returns its listener. It FAILS
// if anything already holds the name: the first instance is created with
// FILE_FLAG_FIRST_PIPE_INSTANCE, and the service treats this error as a
// failed start (service.go) rather than serving under a name another process
// controls.
func ListenPipe(name string) (net.Listener, error) {
	return listenPipe(name, pipeSDDL, pipeMode)
}

func securityAttributes(sddl string) (*windows.SecurityAttributes, error) {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("the pipe's security descriptor: %w", err)
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}, nil
}

func createPipeInstance(name string, sa *windows.SecurityAttributes, first bool, mode uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	flags := uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED)
	if first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	return windows.CreateNamedPipe(p, flags, mode, windows.PIPE_UNLIMITED_INSTANCES, pipeBuffer, pipeBuffer, 0, sa)
}

func listenPipe(name, sddl string, mode uint32) (*pipeListener, error) {
	sa, err := securityAttributes(sddl)
	if err != nil {
		return nil, err
	}
	h, err := createPipeInstance(name, sa, true, mode)
	if err != nil {
		return nil, fmt.Errorf("could not create the local API pipe %s (another process may already hold the name): %w", name, err)
	}
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("the local API pipe's event: %w", err)
	}
	return &pipeListener{name: name, sa: sa, mode: mode, event: ev, ov: new(windows.Overlapped), pending: h}, nil
}

// pipeListener is a net.Listener over pipe instances.
//
// There is ALWAYS an instance open while it is: the next one is created
// before a connected one is handed out, and a failed one is replaced before
// it is closed. A pipe's name exists only while some instance does, and a
// moment without one is a moment another process could create the name.
type pipeListener struct {
	name  string
	sa    *windows.SecurityAttributes
	mode  uint32
	event windows.Handle
	// ov is on the heap and lives as long as the listener: the kernel holds
	// its address for the whole of a pending connect, and a goroutine's
	// stack can move.
	ov *windows.Overlapped

	mu        sync.Mutex
	pending   windows.Handle // the instance the next client connects to
	accepting bool
	closed    bool
}

// temporaryError makes http.Serve retry (with backoff) instead of stopping.
type temporaryError struct{ error }

func (temporaryError) Timeout() bool   { return false }
func (temporaryError) Temporary() bool { return true }

// Accept waits for the next client. Only a closed listener ends it: a client
// that fails mid-connect costs its instance, not the server.
func (l *pipeListener) Accept() (net.Conn, error) {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, net.ErrClosed
		}
		if l.pending == windows.InvalidHandle {
			h, err := createPipeInstance(l.name, l.sa, false, l.mode)
			if err != nil {
				l.mu.Unlock()
				return nil, temporaryError{fmt.Errorf("could not create a local API pipe instance: %w", err)}
			}
			l.pending = h
		}
		h := l.pending
		l.accepting = true
		l.mu.Unlock()

		err := l.connect(h)

		l.mu.Lock()
		l.accepting = false
		if l.closed {
			l.pending = windows.InvalidHandle
			l.release(h)
			l.mu.Unlock()
			return nil, net.ErrClosed
		}
		next, nerr := createPipeInstance(l.name, l.sa, false, l.mode)
		if nerr != nil {
			log.Printf("[API] could not create the next local API pipe instance (retrying on the next accept): %v", nerr)
			next = windows.InvalidHandle
		}
		l.pending = next
		l.mu.Unlock()

		if err != nil {
			log.Printf("[API] a local API client did not connect: %v", err)
			_ = windows.DisconnectNamedPipe(h)
			_ = windows.CloseHandle(h)
			continue
		}
		return newPipeConn(h, l.name, true), nil
	}
}

func (l *pipeListener) connect(h windows.Handle) error {
	if err := windows.ResetEvent(l.event); err != nil {
		return err
	}
	*l.ov = windows.Overlapped{HEvent: l.event}
	err := windows.ConnectNamedPipe(h, l.ov)
	switch {
	case err == nil, errors.Is(err, windows.ERROR_PIPE_CONNECTED):
		// Connected already: a client opened the instance between its
		// creation and this call.
		return nil
	case errors.Is(err, windows.ERROR_IO_PENDING):
	default:
		return err
	}
	var n uint32
	return windows.GetOverlappedResult(h, l.ov, &n, true)
}

// release closes the pending instance and the event once nothing waits on
// them. Called with l.mu held.
func (l *pipeListener) release(h windows.Handle) {
	if h != windows.InvalidHandle {
		_ = windows.CloseHandle(h)
	}
	if l.event != 0 {
		_ = windows.CloseHandle(l.event)
		l.event = 0
	}
}

// Close stops the listener. A pending connect is cancelled, and Accept closes
// what it was using; connections already handed out are their owners'.
func (l *pipeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.accepting {
		_ = windows.CancelIoEx(l.pending, nil)
		return nil
	}
	h := l.pending
	l.pending = windows.InvalidHandle
	l.release(h)
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr(l.name) }

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

// --- a connection --------------------------------------------------------------

// pipeConn is one end of a connected pipe instance. On the service's side it
// also knows its client: identity is read once, after the first bytes arrive
// (Windows refuses to impersonate a client that has not written), which is
// before net/http has parsed a request from them.
type pipeConn struct {
	f    *os.File
	h    windows.Handle // f's handle, for the pipe calls os.File does not make
	name string

	server bool
	once   sync.Once
	who    *Caller
	whoErr error
	// tok is the client's token, kept while the connection is open: phase P3
	// acts as the caller with it. One request per connection, so the
	// connection's lifetime is the request's.
	tok windows.Token
}

func newPipeConn(h windows.Handle, name string, server bool) *pipeConn {
	return &pipeConn{f: os.NewFile(uintptr(h), name), h: h, name: name, server: server}
}

func (c *pipeConn) Read(b []byte) (int, error) {
	n, err := c.f.Read(b)
	if n > 0 && c.server {
		c.once.Do(c.identify)
	}
	return n, err
}

func (c *pipeConn) Write(b []byte) (int, error) { return c.f.Write(b) }

func (c *pipeConn) Close() error {
	// Wait out (or forestall) identification, so the token is not closed
	// while it is being read.
	c.once.Do(func() {
		if c.server && c.whoErr == nil && c.who == nil {
			c.whoErr = errors.New("the connection closed before its client was identified")
		}
	})
	err := c.f.Close()
	if c.tok != 0 {
		_ = c.tok.Close()
		c.tok = 0
	}
	return err
}

func (c *pipeConn) LocalAddr() net.Addr                { return pipeAddr(c.name) }
func (c *pipeConn) RemoteAddr() net.Addr               { return pipeAddr(c.name) }
func (c *pipeConn) SetDeadline(t time.Time) error      { return c.f.SetDeadline(t) }
func (c *pipeConn) SetReadDeadline(t time.Time) error  { return c.f.SetReadDeadline(t) }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { return c.f.SetWriteDeadline(t) }

func (c *pipeConn) identify() {
	c.who, c.tok, c.whoErr = identifyPipeClient(c.h)
}

// caller is callerSource: the identity Windows gave for this connection.
func (c *pipeConn) caller() (*Caller, error) {
	if !c.server {
		return nil, errors.New("a client connection has no caller")
	}
	c.once.Do(c.identify)
	if c.whoErr != nil {
		return nil, c.whoErr
	}
	who := *c.who
	return &who, nil
}

// identifyPipeClient reads the identity of the client connected to h.
func identifyPipeClient(h windows.Handle) (*Caller, windows.Token, error) {
	tok, err := pipeClientToken(h)
	if err != nil {
		return nil, 0, err
	}
	who, err := describeToken(tok)
	if err != nil {
		_ = tok.Close()
		return nil, 0, err
	}
	var pid uint32
	if windows.GetNamedPipeClientProcessId(h, &pid) == nil {
		who.PID = pid
		who.Image = processImage(pid)
	}
	return who, tok, nil
}

// pipeClientToken impersonates the pipe's client for exactly as long as it
// takes to open the thread's token, then reverts.
//
// The goroutine is locked to its thread throughout: impersonation belongs to
// the OS thread, and the runtime must not move this goroutine, or hand the
// thread to another, while it is someone else.
func pipeClientToken(h windows.Handle) (windows.Token, error) {
	runtime.LockOSThread()
	if err := impersonateNamedPipeClient(h); err != nil {
		runtime.UnlockOSThread()
		return 0, fmt.Errorf("could not impersonate the local API client: %w", err)
	}
	var tok windows.Token
	// openAsSelf: open the token with the SERVICE's rights. The client's
	// token is identification-level, and cannot be used to open anything.
	openErr := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, true, &tok)
	if err := windows.RevertToSelf(); err != nil {
		// The thread is still the client. It stays locked to this
		// goroutine, so the runtime discards it when the goroutine ends
		// rather than giving it to other work; the request is refused.
		if openErr == nil {
			_ = tok.Close()
		}
		return 0, fmt.Errorf("could not stop impersonating the local API client: %w", err)
	}
	runtime.UnlockOSThread()
	if openErr != nil {
		return 0, fmt.Errorf("could not open the local API client's token: %w", openErr)
	}
	return tok, nil
}

// describeToken reads a Caller from a token. Any field it cannot read is an
// error, not a default: an identity with a guessed field is not an identity.
func describeToken(tok windows.Token) (*Caller, error) {
	u, err := tok.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("the client's user: %w", err)
	}
	var session, n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), uint32(unsafe.Sizeof(session)), &n); err != nil {
		return nil, fmt.Errorf("the client's session: %w", err)
	}
	elevated, err := tokenElevated(tok)
	if err != nil {
		return nil, err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	// CheckTokenMembership: present AND enabled. A UAC-filtered
	// administrator's token holds the group as deny-only, which is not
	// membership for this purpose.
	admin, err := tok.IsMember(admins)
	if err != nil {
		return nil, fmt.Errorf("the client's Administrators membership: %w", err)
	}
	who := &Caller{UserSID: u.User.Sid.String(), SessionID: session, Elevated: elevated, Admin: admin}
	if account, domain, _, err := u.User.Sid.LookupAccount(""); err == nil {
		who.Account = account
		if domain != "" {
			who.Account = domain + `\` + account
		}
	}
	return who, nil
}

// tokenElevated is TokenElevation. x/sys's Token.IsElevated answers false on
// an error; here an error is an error.
func tokenElevated(tok windows.Token) (bool, error) {
	var elevated, n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), uint32(unsafe.Sizeof(elevated)), &n); err != nil {
		return false, fmt.Errorf("the client's elevation: %w", err)
	}
	return elevated != 0, nil
}

// processImage is a process's executable, for the log. Best effort.
func processImage(pid uint32) string {
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer func() { _ = windows.CloseHandle(p) }()
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(p, 0, &buf[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}

// --- the console's side ----------------------------------------------------------

// pipeCheck is what the console requires of the process serving the pipe
// before it sends a byte (§3, "The client checks the server").
type pipeCheck struct {
	// owner is the SID that must own the pipe. The service creates it owned
	// by SYSTEM (pipeSDDL), and a process that is not SYSTEM cannot.
	owner string
	// servicePID is the process that must be serving it.
	servicePID func() (uint32, error)
}

// serviceCheck is the production check: the pipe is owned by SYSTEM, and the
// process serving it is the one the Service Control Manager reports for the
// NimbusBackup service.
//
// The specification names the executable's path (%ProgramFiles%\NimbusBackup).
// The SCM's process id is the stronger form of the same check and is used
// instead: the installer lets the install folder be chosen, so a fixed path
// would refuse a legitimate install; and a standard user cannot open a
// SYSTEM process to read its path at all, whereas any interactive user may
// query a service's status. Only an administrator can register or repoint a
// service, and §7 already places a local administrator beyond any design.
func serviceCheck() pipeCheck {
	return pipeCheck{
		owner:      systemSID,
		servicePID: func() (uint32, error) { return servicePID(ServiceName) },
	}
}

func (pc pipeCheck) verify(h windows.Handle) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("%w: its owner could not be read: %v", ErrServerNotTrusted, err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("%w: the pipe has no owner", ErrServerNotTrusted)
	}
	if got := owner.String(); got != pc.owner {
		return fmt.Errorf("%w: the pipe belongs to %s, not %s", ErrServerNotTrusted, got, pc.owner)
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(h, &pid); err != nil {
		return fmt.Errorf("%w: its process could not be identified: %v", ErrServerNotTrusted, err)
	}
	want, err := pc.servicePID()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrServerNotTrusted, err)
	}
	if pid != want {
		return fmt.Errorf("%w: it is served by process %d, and the %s service is process %d", ErrServerNotTrusted, pid, ServiceName, want)
	}
	return nil
}

// servicePID is the process id the Service Control Manager reports for a
// service, or an error when it has none (stopped, or not installed).
func servicePID(name string) (uint32, error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, fmt.Errorf("the service manager: %w", err)
	}
	defer func() { _ = windows.CloseServiceHandle(m) }()
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	s, err := windows.OpenService(m, n, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return 0, fmt.Errorf("the %s service: %w", name, err)
	}
	defer func() { _ = windows.CloseServiceHandle(s) }()
	var st windows.SERVICE_STATUS_PROCESS
	var need uint32
	if err := windows.QueryServiceStatusEx(s, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&st)), uint32(unsafe.Sizeof(st)), &need); err != nil {
		return 0, fmt.Errorf("the %s service's status: %w", name, err)
	}
	if st.ProcessId == 0 {
		return 0, fmt.Errorf("the %s service is not running", name)
	}
	return st.ProcessId, nil
}

// dialPipe opens the pipe as the console does, waiting up to timeout for a
// free instance, and refuses a server that fails check. Nothing has been
// written when it returns an error.
func dialPipe(name string, check pipeCheck, timeout time.Duration) (net.Conn, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	var h windows.Handle
	for {
		h, err = windows.CreateFile(p, clientAccess, 0, nil, windows.OPEN_EXISTING, clientFlags, 0)
		if err == nil {
			break
		}
		left := time.Until(deadline)
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) || left <= 0 {
			return nil, fmt.Errorf("could not open the local API pipe %s: %w", name, err)
		}
		// Every instance is serving someone: wait for one to free up.
		waitNamedPipe(p, left)
	}
	if err := check.verify(h); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	return newPipeConn(h, name, false), nil
}
