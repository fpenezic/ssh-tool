package ssh

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// latencyPipe delivers every write after a fixed delay without blocking
// the writer - a link with bandwidth to spare and a real RTT, which is
// the case where the number of packets in flight decides throughput.
func latencyPipe(delay time.Duration) (io.ReadCloser, io.WriteCloser) {
	pr, pw := io.Pipe()
	type chunk struct {
		at time.Time
		b  []byte
	}
	ch := make(chan chunk, 4096)
	go func() {
		for c := range ch {
			time.Sleep(time.Until(c.at))
			if _, err := pw.Write(c.b); err != nil {
				return
			}
		}
		pw.Close()
	}()
	w := &delayWriter{}
	w.send = func(b []byte) { ch <- chunk{time.Now().Add(delay), b} }
	w.close = func() { close(ch) }
	return pr, w
}

type delayWriter struct {
	mu     sync.Mutex
	closed bool
	send   func([]byte)
	close  func()
}

func (w *delayWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	w.send(append([]byte(nil), b...))
	return len(b), nil
}

func (w *delayWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		w.close()
	}
	return nil
}

// sftpOverLatency starts an in-process SFTP server rooted at the real
// filesystem behind a link with the given one-way delay, and returns a
// Session whose cached client talks to it.
func sftpOverLatency(t *testing.T, oneWay time.Duration) *Session {
	t.Helper()
	c2sR, c2sW := latencyPipe(oneWay)
	s2cR, s2cW := latencyPipe(oneWay)
	srv, err := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{c2sR, s2cW})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	cli, err := sftp.NewClientPipe(s2cR, c2sW)
	if err != nil {
		t.Fatal(err)
	}
	// Tear the links down first: each side's receive loop blocks on a pipe
	// read that only a close on the far end would end.
	t.Cleanup(func() {
		c2sR.Close()
		s2cR.Close()
		c2sW.Close()
		s2cW.Close()
		cli.Close()
		srv.Close()
	})
	return &Session{sftp: &sftpHolder{client: cli}}
}

func writeRandom(t *testing.T, path string, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, _ = rand.Read(b)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSftpTransfersRoundTripAndReportProgress(t *testing.T) {
	s := sftpOverLatency(t, 0)
	dir := t.TempDir()
	want := writeRandom(t, filepath.Join(dir, "src"), 3<<20+123)

	var last int64
	n, err := s.SftpUpload(filepath.Join(dir, "src"), filepath.Join(dir, "remote"), func(w, _ int64) { last = w }, nil)
	if err != nil || n != int64(len(want)) {
		t.Fatalf("upload: n=%d err=%v", n, err)
	}
	if last == 0 {
		t.Error("upload reported no progress")
	}
	last = 0
	n, err = s.SftpDownload(filepath.Join(dir, "remote"), filepath.Join(dir, "back"), func(w, _ int64) { last = w }, nil)
	if err != nil || n != int64(len(want)) {
		t.Fatalf("download: n=%d err=%v", n, err)
	}
	if last == 0 {
		t.Error("download reported no progress")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "back"))
	if !bytes.Equal(got, want) {
		t.Fatal("round trip corrupted the file")
	}
}

func TestSftpTransfersHonourCancel(t *testing.T) {
	s := sftpOverLatency(t, 0)
	dir := t.TempDir()
	writeRandom(t, filepath.Join(dir, "src"), 4<<20)
	cancel := make(chan struct{})
	close(cancel)
	if _, err := s.SftpUpload(filepath.Join(dir, "src"), filepath.Join(dir, "remote"), func(_, _ int64) {}, cancel); !errors.Is(err, ErrTransferCancelled) {
		t.Errorf("upload: want ErrTransferCancelled, got %v", err)
	}
	if _, err := s.SftpDownload(filepath.Join(dir, "src"), filepath.Join(dir, "back"), func(_, _ int64) {}, cancel); !errors.Is(err, ErrTransferCancelled) {
		t.Errorf("download: want ErrTransferCancelled, got %v", err)
	}
}

// The regression this guards: a Read/Write loop keeps two 32 KB packets
// in flight, so on any real RTT it runs at a fraction of the link. The
// ratio, not an absolute time, so a loaded CI box does not flake it.
func TestSftpTransfersPipelineOverLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	s := sftpOverLatency(t, 5*time.Millisecond) // 10 ms RTT
	dir := t.TempDir()
	const size = 4 << 20
	writeRandom(t, filepath.Join(dir, "src"), size)
	cli, _ := s.SFTPClient()

	// The old path: Read into a 64 KB buffer.
	t0 := time.Now()
	f, err := cli.Open(filepath.Join(dir, "src"))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64*1024)
	for {
		_, err := f.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	loop := time.Since(t0)

	t0 = time.Now()
	if _, err := s.SftpDownload(filepath.Join(dir, "src"), filepath.Join(dir, "down"), func(_, _ int64) {}, nil); err != nil {
		t.Fatal(err)
	}
	down := time.Since(t0)

	t0 = time.Now()
	if _, err := s.SftpUpload(filepath.Join(dir, "src"), filepath.Join(dir, "up"), func(_, _ int64) {}, nil); err != nil {
		t.Fatal(err)
	}
	up := time.Since(t0)

	mbps := func(d time.Duration) float64 { return float64(size) / d.Seconds() / (1 << 20) }
	t.Logf("10 ms RTT, 4 MB: read loop %v (%.1f MB/s), download %v (%.1f MB/s), upload %v (%.1f MB/s)",
		loop, mbps(loop), down, mbps(down), up, mbps(up))
	if down*3 > loop {
		t.Errorf("download not pipelined: %v vs read loop %v", down, loop)
	}
	if up*3 > loop {
		t.Errorf("upload not pipelined: %v vs read loop %v", up, loop)
	}
}

