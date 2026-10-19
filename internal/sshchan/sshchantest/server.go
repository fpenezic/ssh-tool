// Package sshchantest runs an in-process SSH server for tests of tabs
// that dial through sshchan: it serves direct-tcpip only and resolves
// names from its own table, the way a bastion resolves names this machine
// cannot.
package sshchantest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"

	"ssh-tool/internal/sshchan"
)

// Server is a test SSH server serving direct-tcpip.
type Server struct {
	Addr  string
	hosts map[string]string // name -> address to dial

	mu    sync.Mutex
	conns []net.Conn
	dials []string
}

// DropAll closes every client connection, as a dropped network would.
func (s *Server) DropAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.conns = nil
}

// Dialed lists the host:port of every direct-tcpip request, as asked.
func (s *Server) Dialed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.dials...)
}

// Start runs a server until the test ends. hosts maps names only the
// server knows to the address it dials instead.
func Start(t *testing.T, hosts map[string]string) *Server {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &Server{Addr: ln.Addr().String(), hosts: hosts}
	t.Cleanup(srv.DropAll)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			srv.mu.Lock()
			srv.conns = append(srv.conns, c)
			srv.mu.Unlock()
			go srv.serve(c, cfg)
		}
	}()
	return srv
}

func (s *Server) serve(c net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			_ = nc.Reject(ssh.Prohibited, "no "+nc.ChannelType())
			continue
		}
		extra := nc.ExtraData()
		hl := binary.BigEndian.Uint32(extra)
		host := string(extra[4 : 4+hl])
		port := binary.BigEndian.Uint32(extra[4+hl:])
		target := net.JoinHostPort(host, strconv.Itoa(int(port)))
		if mapped, ok := s.hosts[host]; ok {
			target = net.JoinHostPort(mapped, strconv.Itoa(int(port)))
		}
		s.mu.Lock()
		s.dials = append(s.dials, net.JoinHostPort(host, strconv.Itoa(int(port))))
		s.mu.Unlock()
		up, err := net.Dial("tcp", target)
		if err != nil {
			_ = nc.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			_ = up.Close()
			continue
		}
		go ssh.DiscardRequests(creqs)
		go func() { _, _ = io.Copy(ch, up); _ = ch.CloseWrite() }()
		go func() { _, _ = io.Copy(up, ch); _ = up.Close() }()
	}
}

// Lazy returns a LazySSH dialing s, counting connects.
func (s *Server) Lazy(connects *atomic.Int32) *sshchan.LazySSH {
	return &sshchan.LazySSH{Connect: func(ctx context.Context) (*ssh.Client, func(), error) {
		connects.Add(1)
		var d net.Dialer
		raw, err := d.DialContext(ctx, "tcp", s.Addr)
		if err != nil {
			return nil, nil, err
		}
		cc, chans, reqs, err := ssh.NewClientConn(raw, s.Addr, &ssh.ClientConfig{
			User:            "test",
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		})
		if err != nil {
			_ = raw.Close()
			return nil, nil, err
		}
		c := ssh.NewClient(cc, chans, reqs)
		return c, func() { _ = c.Close() }, nil
	}}
}
