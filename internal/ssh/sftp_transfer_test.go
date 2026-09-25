package ssh

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

	// Best of three for the fast paths. They are CPU-bound where the read
	// loop is latency-bound, so on a busy CI runner (packages testing in
	// parallel) one run can come out slow for reasons that have nothing
	// to do with pipelining - a v0.103.0 release run measured 2.2x once,
	// against 9x locally every time.
	best := func(run func(i int) error) time.Duration {
		var min time.Duration
		for i := 0; i < 3; i++ {
			t0 := time.Now()
			if err := run(i); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(t0); i == 0 || d < min {
				min = d
			}
		}
		return min
	}
	down := best(func(i int) error {
		_, err := s.SftpDownload(filepath.Join(dir, "src"), filepath.Join(dir, fmt.Sprintf("down%d", i)), func(_, _ int64) {}, nil)
		return err
	})
	up := best(func(i int) error {
		_, err := s.SftpUpload(filepath.Join(dir, "src"), filepath.Join(dir, fmt.Sprintf("up%d", i)), func(_, _ int64) {}, nil)
		return err
	})

	mbps := func(d time.Duration) float64 { return float64(size) / d.Seconds() / (1 << 20) }
	t.Logf("10 ms RTT, 4 MB: read loop %v (%.1f MB/s), download %v (%.1f MB/s), upload %v (%.1f MB/s)",
		loop, mbps(loop), down, mbps(down), up, mbps(up))
	// Unpipelined, a transfer runs at the read loop's speed (ratio ~1);
	// pipelined it is ~9x faster here. 2x separates the two with room for
	// a slow machine.
	if down*2 > loop {
		t.Errorf("download not pipelined: %v vs read loop %v", down, loop)
	}
	if up*2 > loop {
		t.Errorf("upload not pipelined: %v vs read loop %v", up, loop)
	}
}

// firstProgress records the first figure a transfer reports: a resumed
// transfer starts past the .part, a restarted one near zero.
func firstProgress() (func(int64, int64), *int64) {
	first := int64(-1)
	return func(w, _ int64) {
		if first < 0 {
			first = w
		}
	}, &first
}

func TestSftpTransfersResumeFromPart(t *testing.T) {
	s := sftpOverLatency(t, 0)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	want := writeRandom(t, src, 3<<20)
	half := int64(len(want) / 2)

	for _, dirn := range []string{"download", "upload"} {
		t.Run(dirn, func(t *testing.T) {
			dest := filepath.Join(dir, dirn)
			if err := os.WriteFile(dest+PartSuffix, want[:half], 0o600); err != nil {
				t.Fatal(err)
			}
			cb, first := firstProgress()
			var n int64
			var err error
			if dirn == "download" {
				n, err = s.SftpDownload(src, dest, cb, nil)
			} else {
				n, err = s.SftpUpload(src, dest, cb, nil)
			}
			if err != nil || n != int64(len(want)) {
				t.Fatalf("n=%d err=%v", n, err)
			}
			if *first <= half {
				t.Errorf("did not resume: first progress %d, .part held %d", *first, half)
			}
			got, _ := os.ReadFile(dest)
			if !bytes.Equal(got, want) {
				t.Fatal("resumed file differs from the source")
			}
			if _, err := os.Stat(dest + PartSuffix); !os.IsNotExist(err) {
				t.Errorf(".part left behind after success: %v", err)
			}
		})
	}
}

func TestSftpTransfersRestartOnForeignPart(t *testing.T) {
	s := sftpOverLatency(t, 0)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	want := writeRandom(t, src, 1<<20)

	cases := map[string][]byte{
		"different content": bytes.Repeat([]byte{0xAA}, len(want)/2),
		"longer than source": append(append([]byte(nil), want...), 1, 2, 3),
	}
	for name, part := range cases {
		t.Run(name, func(t *testing.T) {
			dest := filepath.Join(dir, "d-"+name)
			if err := os.WriteFile(dest+PartSuffix, part, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SftpDownload(src, dest, func(_, _ int64) {}, nil); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(dest)
			if !bytes.Equal(got, want) {
				t.Fatal("a foreign .part leaked into the result")
			}
		})
	}
}

func TestSftpCancelLeavesOnlyPart(t *testing.T) {
	s := sftpOverLatency(t, 0)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeRandom(t, src, 1<<20)
	dest := filepath.Join(dir, "dest")
	cancel := make(chan struct{})
	close(cancel)
	if _, err := s.SftpDownload(src, dest, func(_, _ int64) {}, cancel); !errors.Is(err, ErrTransferCancelled) {
		t.Fatalf("want ErrTransferCancelled, got %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("a cancelled download left a file under the real name")
	}
	if _, err := os.Stat(dest + PartSuffix); err != nil {
		t.Errorf("no .part to resume from: %v", err)
	}
}

func TestSftpUploadReplacesExistingTarget(t *testing.T) {
	s := sftpOverLatency(t, 0)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	want := writeRandom(t, src, 200<<10)
	dest := filepath.Join(dir, "dest")
	if err := os.WriteFile(dest, []byte("old contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SftpUpload(src, dest, func(_, _ int64) {}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, want) {
		t.Fatal("existing target not replaced")
	}
}

func TestExplainSFTPOpenError(t *testing.T) {
	closed := fmt.Errorf("error receiving version packet from server: %w",
		fmt.Errorf("server unexpectedly closed connection: %w", io.ErrUnexpectedEOF))
	for name, in := range map[string]error{
		"closed before version": closed,
		"subsystem refused":     errors.New("ssh: subsystem request failed"),
	} {
		t.Run(name, func(t *testing.T) {
			got := explainSFTPOpenError(in)
			if !errors.Is(got, ErrNoSFTPSubsystem) {
				t.Fatalf("not recognised as a missing subsystem: %v", got)
			}
			if !strings.Contains(got.Error(), "SSH works") {
				t.Errorf("message does not say SSH itself is fine: %v", got)
			}
		})
	}
	other := explainSFTPOpenError(errors.New("ssh: handshake failed"))
	if errors.Is(other, ErrNoSFTPSubsystem) {
		t.Errorf("an unrelated failure was blamed on the subsystem: %v", other)
	}
}
