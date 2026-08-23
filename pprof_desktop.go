//go:build !android && !ios

package main

import (
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"
)

// startPprof exposes Go's profiler on localhost when SSH_TOOL_PPROF is set
// to a port ("6060") or host:port.
//
// It exists because the last memory investigation was guesswork off two
// Task Manager screenshots, and the first guess (terminal scrollback) was
// wrong - the memory was in the GPU process holding one WebGL context per
// terminal. Task Manager can tell you a process is large; it cannot tell
// you which allocation is responsible. This can:
//
//	SSH_TOOL_PPROF=6060 ssh-tool
//	go tool pprof -http=: http://localhost:6060/debug/pprof/heap
//	curl -s localhost:6060/debug/pprof/heap > before.pb.gz   # diff two points
//
// Opt-in and localhost-only on purpose. The pprof endpoints expose command
// line, goroutine stacks and heap contents; this app holds credentials in
// memory, so it must never be reachable by default or from off-box. There
// is deliberately no setting for it - an env var cannot be flipped on by a
// stray click or a synced settings row.
func startPprof() {
	addr := os.Getenv("SSH_TOOL_PPROF")
	if addr == "" {
		return
	}
	// A bare port means loopback. Anything else is taken as given, which
	// allows a deliberate 0.0.0.0 bind for profiling from another machine
	// but never happens by accident.
	if _, err := os.Stat(addr); err != nil && !containsColon(addr) {
		addr = "127.0.0.1:" + addr
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// A plain-text summary for the common case of "is it growing?", which
	// does not need the pprof toolchain installed to answer.
	mux.HandleFunc("/debug/memstats", func(w http.ResponseWriter, _ *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(
			"heap_alloc_mb  " + mb(m.HeapAlloc) + "\n" +
				"heap_sys_mb    " + mb(m.HeapSys) + "\n" +
				"heap_idle_mb   " + mb(m.HeapIdle) + "\n" +
				"heap_released_mb " + mb(m.HeapReleased) + "\n" +
				"stack_sys_mb   " + mb(m.StackSys) + "\n" +
				"sys_total_mb   " + mb(m.Sys) + "\n" +
				"num_gc         " + strconv.FormatUint(uint64(m.NumGC), 10) + "\n" +
				"goroutines     " + strconv.Itoa(runtime.NumGoroutine()) + "\n"))
	})

	// The app's own process tree with each process's working set. Go's heap
	// numbers above describe only the Go side, which is the small half: on
	// Windows the WebView2 host processes hold most of the memory and pprof
	// cannot see them at all. This answers "how much RAM with N tabs open"
	// without anyone having to work out which msedgewebview2.exe rows in Task
	// Manager belong to this app.
	mux.HandleFunc("/debug/procmem", func(w http.ResponseWriter, _ *http.Request) {
		procs, err := procTree()
		if err != nil {
			http.Error(w, "procmem: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(formatProcTree(procs)))
	})

	// Force a GC + return free pages to the OS. Distinguishes "we are
	// holding this memory" from "the runtime has not handed it back yet",
	// which is most of the gap between Go's heap numbers and what the OS
	// process list shows.
	mux.HandleFunc("/debug/freeosmemory", func(w http.ResponseWriter, _ *http.Request) {
		debug.FreeOSMemory()
		_, _ = w.Write([]byte("ok\n"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("pprof listening on http://%s/debug/pprof/", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("pprof: %v", err)
		}
	}()
}

func containsColon(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return true
		}
	}
	return false
}

func mb(b uint64) string { return strconv.FormatUint(b/1024/1024, 10) }
