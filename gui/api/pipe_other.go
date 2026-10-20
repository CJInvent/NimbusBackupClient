//go:build !windows

package api

import (
	"net"
	"time"
)

// ListenPipe has nothing to listen on outside Windows (ErrPipeUnsupported).
func ListenPipe(name string) (net.Listener, error) { return nil, ErrPipeUnsupported }

// pipeCheck exists so serviceCheck has a type on every system.
type pipeCheck struct{}

func serviceCheck() pipeCheck { return pipeCheck{} }

func dialPipe(name string, check pipeCheck, timeout time.Duration) (net.Conn, error) {
	return nil, ErrPipeUnsupported
}
