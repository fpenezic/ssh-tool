package share

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Each pending connection gets its own join code, delivered in its Pending
// frame and handed to that connection's approval prompt. The certificate
// words are the same for everyone, so they cannot tell two holders of one
// link apart; the join code can.
func TestJoinCodePerConnection(t *testing.T) {
	dir := t.TempDir()
	cert, err := LoadOrCreate(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	prompted := map[string]bool{}
	srv := NewServer(Config{
		Resolve: func(string) (Sourced, bool) { return nil, false },
		Approve: func(_, _, joinCode, _ string) bool {
			mu.Lock()
			prompted[joinCode] = true
			mu.Unlock()
			// A real host takes seconds; deny only after the pending
			// frame has gone out.
			time.Sleep(200 * time.Millisecond)
			return false
		},
	})
	share := newShareSession("sh1", "127.0.0.1:0", "host", LevelControl, true, 0, nil,
		[]SharedSession{{Slot: "s1", RealID: "real1", Name: "n"}})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.handleWS(w, r, share, r.Host, cert)
	}))
	defer hs.Close()

	codes := map[string]bool{}
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hs.URL, "http"), nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		_, data, err := c.Read(ctx)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		var f Frame
		if err := json.Unmarshal(data, &f); err != nil || f.T != TPending || f.Pending == nil {
			cancel()
			t.Fatalf("want a pending frame, got %s (%v)", data, err)
		}
		if len(strings.Split(f.Pending.JoinCode, "-")) != 4 {
			t.Fatalf("join code %q is not four words", f.Pending.JoinCode)
		}
		if f.Pending.FpWords != cert.Fingerprint.Words {
			t.Fatalf("fingerprint words changed: %q", f.Pending.FpWords)
		}
		codes[f.Pending.JoinCode] = true
		// The deny closes the socket; reading to the end waits for the
		// approval call to have run.
		for {
			if _, _, err := c.Read(ctx); err != nil {
				break
			}
		}
		cancel()
	}
	if len(codes) != 2 {
		t.Fatalf("two connections got the same join code: %v", codes)
	}
	mu.Lock()
	defer mu.Unlock()
	for code := range codes {
		if !prompted[code] {
			t.Fatalf("approval prompt did not receive join code %q (got %v)", code, prompted)
		}
	}
}
