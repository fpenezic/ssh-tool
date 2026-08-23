//go:build !android && !ios

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// procInfo is one process in the app's tree.
type procInfo struct {
	PID        int
	PPID       int
	Name       string
	WorkingSet uint64 // bytes resident; the number Task Manager shows
}

// procTree returns this process plus every descendant of it, which on Windows
// means the WebView2 host processes Wails spawns.
//
// This exists because the app's memory does not live where Go can see it. The
// Go heap idles around 35 MB with ~2 MB live; the rest is in the WebView2
// browser/GPU/renderer processes, which pprof knows nothing about. Answering
// "how much RAM does this use with 20 tabs open" therefore meant reading Task
// Manager and manually working out which of a dozen msedgewebview2.exe rows
// belong to this app - error-prone enough that a previous investigation drew
// the wrong conclusion from it.
//
// The app knows its own PID, so it can just enumerate the tree itself.
func procTree() ([]procInfo, error) {
	all, err := listProcesses()
	if err != nil {
		return nil, err
	}
	byParent := map[int][]procInfo{}
	for _, p := range all {
		byParent[p.PPID] = append(byParent[p.PPID], p)
	}
	self := os.Getpid()

	// Breadth-first from our own PID. A visited set guards against a PID that
	// reports itself as its own ancestor, which a recycled PID can produce and
	// which would otherwise spin here forever.
	var out []procInfo
	seen := map[int]bool{self: true}
	queue := []int{self}
	for _, p := range all {
		if p.PID == self {
			out = append(out, p)
			break
		}
	}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range byParent[pid] {
			if seen[child.PID] {
				continue
			}
			seen[child.PID] = true
			out = append(out, child)
			queue = append(queue, child.PID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkingSet > out[j].WorkingSet })
	return out, nil
}

// formatProcTree renders the tree as plain text with a total, so reading it
// needs nothing but curl.
func formatProcTree(procs []procInfo) string {
	var b strings.Builder
	var total uint64
	b.WriteString(fmt.Sprintf("%-8s %-8s %-32s %10s\n", "PID", "PPID", "NAME", "RSS_MB"))
	for _, p := range procs {
		total += p.WorkingSet
		b.WriteString(fmt.Sprintf("%-8d %-8d %-32s %10.1f\n",
			p.PID, p.PPID, truncate(p.Name, 32), float64(p.WorkingSet)/1024/1024))
	}
	b.WriteString(fmt.Sprintf("\n%d processes, total %.1f MB\n", len(procs), float64(total)/1024/1024))
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
