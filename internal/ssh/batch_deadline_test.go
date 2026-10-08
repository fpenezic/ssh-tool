package ssh

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

// Silent server: the connect deadline fires. Server that sends its banner
// and then takes its time (slow auth): the deadline moves past it.
func TestBannerDeadlineConn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	banner := make(chan bool, 2)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if <-banner {
				_, _ = c.Write([]byte("SSH-2.0-test\r\n"))
			}
			defer c.Close()
		}
	}()

	read := func(sendBanner bool) (int, error) {
		banner <- sendBanner
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
		c := &bannerDeadlineConn{Conn: conn}
		buf := make([]byte, 64)
		n, err := c.Read(buf)
		if err != nil {
			return n, err
		}
		// The second read waits on a server that sends nothing more. It
		// must outlive the 200ms connect deadline; the conn is closed
		// after 500ms to end it.
		t0 := time.Now()
		go func() { time.Sleep(500 * time.Millisecond); conn.Close() }()
		_, err = c.Read(buf)
		if time.Since(t0) < 450*time.Millisecond {
			t.Errorf("read ended after %v: %v", time.Since(t0), err)
		}
		return n, err
	}

	if _, err := read(false); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("silent server: err = %v, want deadline", err)
	}
	n, err := read(true)
	if n == 0 {
		t.Fatalf("banner not read: %v", err)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("after banner the connect deadline still fired")
	}
}
