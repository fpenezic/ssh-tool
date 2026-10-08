package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	sshlayer "ssh-tool/internal/ssh"
)

// ----- Upload to / download from many -----

// FleetUploadInput is what the Fleet "Upload file" dialog sends.
type FleetUploadInput struct {
	IDs     []string                    `json:"ids"`
	Options sshlayer.FleetUploadOptions `json:"options"`
	// Labels names "session:<id>" hosts (open tabs) for the results and
	// the download folders; saved connections are named from the DB.
	Labels map[string]string `json:"labels,omitempty"`
}

// FleetTransferEvent is emitted on "fleet_upload:<runID>" and
// "fleet_download:<runID>": one host's progress, or Done with every
// host's final state.
type FleetTransferEvent struct {
	Host    *sshlayer.FleetTransferHost  `json:"host,omitempty"`
	Done    bool                         `json:"done,omitempty"`
	Results []sshlayer.FleetTransferHost `json:"results,omitempty"`
}

// FleetUploadStart sends one local file or directory to every host in the
// background. The frontend picks runID and subscribes to
// "fleet_upload:<runID>" BEFORE calling, so no early event is missed.
// SftpCancelTransfer(runID) stops it: hosts not started yet are marked
// cancelled, running transfers abort at their next chunk.
func (a *App) FleetUploadStart(runID string, in FleetUploadInput) error {
	if runID == "" || len(in.IDs) == 0 {
		return fmt.Errorf("no hosts selected")
	}
	if in.Options.LocalPath == "" {
		return fmt.Errorf("choose a file or directory to upload")
	}
	if _, err := os.Stat(in.Options.LocalPath); err != nil {
		return err
	}
	if _, _, err := sshlayer.ParseFleetMode(in.Options.Mode); err != nil {
		return err
	}
	if strings.ContainsAny(in.Options.RemoteDir, "\n\x00") {
		return fmt.Errorf("invalid remote directory")
	}
	switch in.Options.Existing {
	case "", "skip", "overwrite", "changed":
	default:
		return fmt.Errorf("unknown existing-file policy %q", in.Options.Existing)
	}

	cancel := make(chan struct{})
	a.transfersMu.Lock()
	a.transfers[runID] = cancel
	a.transfersMu.Unlock()

	hosts := a.transferHosts(in.IDs, in.Labels)
	event := "fleet_upload:" + runID
	go func() {
		onUpdate := a.fleetProgress(runID, event)
		results := sshlayer.FleetUpload(a.db, a.vault, a.makeHostKeyCallback(), a.makeAlgoLookup(),
			a.batchConnectTimeout(), hosts, in.Options, onUpdate, cancel)
		a.fleetFinished(runID, event, results)
	}()
	return nil
}

// fleetProgress returns the per-host update callback for a fleet
// transfer: emits each host's row on event, throttling progress ticks per
// host (state changes always go), and feeds the taskbar the run's total.
func (a *App) fleetProgress(runID, event string) func(sshlayer.FleetTransferHost) {
	var mu sync.Mutex
	last := map[string]time.Time{}
	bytes := map[string][2]int64{}
	return func(h sshlayer.FleetTransferHost) {
		mu.Lock()
		now := time.Now()
		bytes[h.ConnectionID] = [2]int64{h.Bytes, h.Total}
		if h.State == "transferring" && now.Sub(last[h.ConnectionID]) < 200*time.Millisecond {
			mu.Unlock()
			return
		}
		last[h.ConnectionID] = now
		var done, total int64
		for _, b := range bytes {
			done, total = done+b[0], total+b[1]
		}
		mu.Unlock()
		a.transferProgress(runID, done, total)
		EventsEmit(event, FleetTransferEvent{Host: &h})
	}
}

func (a *App) fleetFinished(runID, event string, results []sshlayer.FleetTransferHost) {
	a.releaseTransfer(runID)
	failed := false
	for _, r := range results {
		failed = failed || r.State == "error"
	}
	a.transferFinished(runID, failed)
	EventsEmit(event, FleetTransferEvent{Done: true, Results: results})
}

// FleetDownloadInput is what the Fleet "Download files" dialog sends.
type FleetDownloadInput struct {
	IDs     []string                      `json:"ids"`
	Options sshlayer.FleetDownloadOptions `json:"options"`
	Labels  map[string]string             `json:"labels,omitempty"`
}

func checkFleetDownload(in FleetDownloadInput) error {
	if len(in.IDs) == 0 {
		return fmt.Errorf("no hosts selected")
	}
	if strings.TrimSpace(in.Options.RemotePath) == "" {
		return fmt.Errorf("give a remote file, folder or pattern")
	}
	if strings.ContainsAny(in.Options.RemotePath, "\n\x00") {
		return fmt.Errorf("invalid remote path")
	}
	return nil
}

// FleetDownloadScan counts what a download would fetch on each host -
// files and bytes - without fetching. The dialog shows it (and warns on
// a large total) before anything is written locally.
func (a *App) FleetDownloadScan(in FleetDownloadInput) ([]sshlayer.FleetTransferHost, error) {
	if err := checkFleetDownload(in); err != nil {
		return nil, err
	}
	return sshlayer.FleetDownloadScan(a.db, a.vault, a.makeHostKeyCallback(), a.makeAlgoLookup(),
		a.batchConnectTimeout(), a.transferHosts(in.IDs, in.Labels), in.Options), nil
}

// FleetDownloadStart fetches the remote path from every host into
// LocalDir/<host>/ in the background; events and cancel as for
// FleetUploadStart, on "fleet_download:<runID>".
func (a *App) FleetDownloadStart(runID string, in FleetDownloadInput) error {
	if runID == "" {
		return fmt.Errorf("no run id")
	}
	if err := checkFleetDownload(in); err != nil {
		return err
	}
	if st, err := os.Stat(in.Options.LocalDir); err != nil || !st.IsDir() {
		return fmt.Errorf("choose an existing local folder to download into")
	}
	cancel := make(chan struct{})
	a.transfersMu.Lock()
	a.transfers[runID] = cancel
	a.transfersMu.Unlock()

	hosts := a.transferHosts(in.IDs, in.Labels)
	dirs := fleetHostDirs(hosts)
	event := "fleet_download:" + runID
	go func() {
		results := sshlayer.FleetDownload(a.db, a.vault, a.makeHostKeyCallback(), a.makeAlgoLookup(),
			a.batchConnectTimeout(), hosts, dirs, in.Options, a.fleetProgress(runID, event), cancel)
		a.fleetFinished(runID, event, results)
	}()
	return nil
}

// fleetHostDirs names each host's download folder after the connection,
// made a legal file name; two hosts with the same name get "-2", "-3"
// (compared case-insensitively, for Windows and macOS).
func fleetHostDirs(hosts []sshlayer.BatchHostInput) map[string]string {
	out := map[string]string{}
	used := map[string]bool{}
	for _, h := range hosts {
		base := sshlayer.SafeFileName(firstNonEmptyStr(h.Name, h.Hostname, "host"))
		name := base
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		used[strings.ToLower(name)] = true
		out[h.ConnectionID] = name
	}
	return out
}

// FleetOpenDir shows a download folder in the OS file manager.
func (a *App) FleetOpenDir(dir string) error {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("folder not found: %s", dir)
	}
	return openDirInFileManager(dir)
}

// transferHosts is batchHosts plus open tabs: "session:<id>" uses that live
// session (its SFTP client) instead of a new connection. A session that has
// gone away resolves to a host the transfer reports as failed.
func (a *App) transferHosts(ids []string, labels map[string]string) []sshlayer.BatchHostInput {
	var saved []string
	for _, id := range ids {
		if !strings.HasPrefix(id, "session:") {
			saved = append(saved, id)
		}
	}
	byID := map[string]sshlayer.BatchHostInput{}
	for _, h := range a.batchHosts(saved) {
		byID[h.ConnectionID] = h
	}
	out := make([]sshlayer.BatchHostInput, 0, len(ids))
	for _, id := range ids {
		sid, isTab := strings.CutPrefix(id, "session:")
		if !isTab {
			out = append(out, byID[id])
			continue
		}
		h := sshlayer.BatchHostInput{ConnectionID: id, Name: firstNonEmptyStr(labels[id], "session")}
		if sess, ok := a.pool.Get(sid); ok {
			h.Session = sess
		}
		out = append(out, h)
	}
	return out
}
