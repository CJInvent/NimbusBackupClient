// Package api is the service's local API and the console's client of it:
// the routes, the wire types, the named pipe they travel over, and the caller
// identity the service reads from it (docs/V4-LOCAL-API.md, docs/V4-CONSOLE.md).
// It is the only code the two programs share.
package api

import "errors"

// The local API's transport (docs/V4-LOCAL-API.md §3): a named pipe, because
// a pipe tells the server who its client is and a loopback port does not.

// PipeName is the service's pipe. The service creates its first instance at
// start with FILE_FLAG_FIRST_PIPE_INSTANCE, so a process that took the name
// first makes the service fail to start rather than be impersonated.
const PipeName = `\\.\pipe\NimbusBackup`

// ServiceName is the Windows service the console expects to find serving the
// pipe (the WiX ServiceInstall's Name, and service_main.go's).
const ServiceName = "NimbusBackup"

// ErrServerNotTrusted is the console refusing a pipe server it cannot prove is
// the NimbusBackup service running as SYSTEM. Nothing is sent to it.
var ErrServerNotTrusted = errors.New("the process serving the local API is not the NimbusBackup service")

// ErrPipeUnsupported is the local API on a system without named pipes. The
// service and the console ship for Windows only; the Linux build exists so the
// linter and the tests see every caller, and it has no local API to serve.
var ErrPipeUnsupported = errors.New("the local API is a Windows named pipe; this system has none")
