package ssh

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

// Minimal SOCKS5 server: handles the connect command (0x01), no auth (0x00),
// over IPv4 / IPv6 / domain destination atyps. Errors out on bind / UDP
// associate, which is the standard subset most clients (Chrome, curl, ssh -D
// itself) ever speak.
//
// Returns the destination address (host:port) to dial through the SSH
// transport. The caller writes the SOCKS5 success reply and then performs
// the byte tunnel.

const (
	socks5Version  = 0x05
	cmdConnect     = 0x01
	atypIPv4       = 0x01
	atypDomain     = 0x03
	atypIPv6       = 0x04
	replySuccess   = 0x00
	replyHostUnreach = 0x04
	replyCmdUnsupp = 0x07
)

// handleSocks5 negotiates with the local client and returns the
// destination address. It also writes the SOCKS5 reply that hands the
// connection over to the tunnel phase.
func handleSocks5(conn net.Conn) (string, error) {
	// --- Method negotiation ---
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", fmt.Errorf("socks5: read header: %w", err)
	}
	if header[0] != socks5Version {
		return "", fmt.Errorf("socks5: bad version %d", header[0])
	}
	nMethods := int(header[1])
	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", fmt.Errorf("socks5: read methods: %w", err)
	}
	// We only support "no auth" (0x00).
	chosen := byte(0xff)
	for _, m := range methods {
		if m == 0x00 {
			chosen = 0x00
			break
		}
	}
	if _, err := conn.Write([]byte{socks5Version, chosen}); err != nil {
		return "", fmt.Errorf("socks5: write method ack: %w", err)
	}
	if chosen == 0xff {
		return "", fmt.Errorf("socks5: no acceptable auth methods")
	}

	// --- Request ---
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		return "", fmt.Errorf("socks5: read request: %w", err)
	}
	if req[0] != socks5Version {
		return "", fmt.Errorf("socks5: bad version %d in request", req[0])
	}
	if req[1] != cmdConnect {
		_ = writeReply(conn, replyCmdUnsupp)
		return "", fmt.Errorf("socks5: only CONNECT supported, got %d", req[1])
	}
	// req[2] is reserved 0x00.
	atyp := req[3]

	var host string
	switch atyp {
	case atypIPv4:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	case atypIPv6:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	case atypDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return "", err
		}
		buf := make([]byte, lenBuf[0])
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		host = string(buf)
	default:
		_ = writeReply(conn, replyHostUnreach)
		return "", fmt.Errorf("socks5: unsupported atyp %d", atyp)
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf)

	if err := writeReply(conn, replySuccess); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d", host, port), nil
}

// writeReply emits the canonical "success" SOCKS5 reply: BND addr/port
// 0.0.0.0:0 since we don't care to surface the actual remote-side bind
// (most clients don't either).
func writeReply(conn net.Conn, status byte) error {
	reply := []byte{socks5Version, status, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0}
	_, err := conn.Write(reply)
	return err
}
