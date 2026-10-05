package ssh

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"syscall"
)

// PortInUseError is a local listen that failed because the address is taken.
// HolderID names the forward in this pool that holds it; empty means some
// other program does. The app layer turns the id into a connection name, so
// the user reads "used by the tunnel on db-bastion" instead of a raw bind
// error.
type PortInUseError struct {
	Addr     string
	Port     uint16
	HolderID string
	// Reserved: Windows refused the port because it sits in an excluded
	// port range (Hyper-V / WSL / Docker reserve blocks of them). Nothing
	// is listening there, so "in use" would be the wrong thing to say.
	Reserved bool
	Err      error
}

func (e *PortInUseError) Error() string {
	switch {
	case e.Reserved:
		return fmt.Sprintf("local port %d is reserved by Windows (excluded port range)", e.Port)
	case e.HolderID != "":
		return fmt.Sprintf("local port %d is already used by another tunnel", e.Port)
	}
	return fmt.Sprintf("local port %d is in use by another program", e.Port)
}

func (e *PortInUseError) Unwrap() error { return e.Err }

// Windows reports its own WSA codes, which are not the invented
// syscall.EADDRINUSE constant Go defines there.
const (
	wsaEACCES     = 10013
	wsaEADDRINUSE = 10048
)

// classifyListenErr reports whether a Listen error means "taken" or, on
// Windows, "reserved".
func classifyListenErr(err error) (inUse, reserved bool) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false, false
	}
	if errno == syscall.EADDRINUSE || errno == wsaEADDRINUSE {
		return true, false
	}
	if runtime.GOOS == "windows" && errno == wsaEACCES {
		return false, true
	}
	return false, false
}

// SameBind reports whether two local listen addresses collide. A wildcard
// bind takes the port on every address, so it collides with anything.
func SameBind(a, b string) bool {
	norm := func(s string) string {
		if s == "" {
			return "127.0.0.1"
		}
		return s
	}
	a, b = norm(a), norm(b)
	wild := func(s string) bool { return s == "0.0.0.0" || s == "::" }
	return a == b || wild(a) || wild(b)
}

// holderOf returns the id of the running local or SOCKS forward bound to
// addr:port, or "". Remote forwards listen on the server, not here.
func (p *ForwardPool) holderOf(addr string, port uint16) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, af := range p.byID {
		if af.kind != ForwardLocal && af.kind != ForwardDynamic {
			continue
		}
		if af.localPort == port && SameBind(af.localAddr, addr) {
			return id
		}
	}
	return ""
}

// PortProbe is what CheckLocalPort found.
type PortProbe struct {
	InUse    bool
	Reserved bool
	HolderID string // running forward in this pool, "" = another program
}

// CheckLocalPort tells whether addr:port could be bound right now, without
// keeping it. A port held by one of our own forwards is reported by id so
// the caller can name it.
func (p *ForwardPool) CheckLocalPort(addr string, port uint16) PortProbe {
	if port == 0 {
		return PortProbe{}
	}
	if addr == "" {
		addr = "127.0.0.1"
	}
	if id := p.holderOf(addr, port); id != "" {
		return PortProbe{InUse: true, HolderID: id}
	}
	l, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(int(port))))
	if err != nil {
		inUse, reserved := classifyListenErr(err)
		return PortProbe{InUse: inUse, Reserved: reserved}
	}
	_ = l.Close()
	return PortProbe{}
}
