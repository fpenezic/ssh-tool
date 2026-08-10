// MCP file tools: let a shared session hand files to the LLM.
//
// The bridge could already read remote files by shelling out (`run` with
// `cat`), but that is useless for anything binary or large: the bytes travel
// through the model's context, and a base64 dump of a 50 MB core file is not a
// thing anyone wants to pay for. These three tools use the SFTP client the
// session already owns.
//
// Grant model - deliberately identical to the shell one in mcpRun, because a
// gate the LLM can walk around by calling `run` instead is friction, not
// safety:
//
//   - list_files   ~ `ls`  : needs read-run, auto (no prompt).
//   - read_file    ~ `cat` : needs read-run, auto (no prompt).
//   - download_file          needs read-run, PROMPTS (YOLO auto-approves).
//
// download_file is the odd one out on purpose. Everything else in the toolset
// only ever moves bytes into the conversation; this one writes them to the
// user's local disk, which no other tool does. That side effect earns a prompt
// of its own even though the same bytes were already readable.
//
// The local destination is never chosen by the LLM. It is always
// <datadir>/mcp-downloads/<session>/<sanitised basename>, so a malicious or
// confused model cannot aim a write at ~/.ssh/authorized_keys or a startup
// folder.

package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

const (
	// mcpReadFileDefaultCap is how much of a file read_file returns when the
	// caller doesn't ask for a size. Small on purpose - the LLM should reach
	// for download_file when it wants the whole thing.
	mcpReadFileDefaultCap = 64 * 1024
	// mcpReadFileHardCap bounds what read_file will ever inline, whatever the
	// caller asks for. Above this, download_file is the answer.
	mcpReadFileHardCap = 1 << 20
	// mcpListFilesCap bounds one directory listing so `ls /usr/lib` can't
	// flood the context.
	mcpListFilesCap = 500
)

// mcpDownloadRoot is the sandbox every LLM-initiated download lands under.
// Inside the data dir so it follows the user profile and is covered by the
// same expectations as the rest of our state.
func mcpDownloadRoot() string {
	return filepath.Join(store.DataDir(), "mcp-downloads")
}

// mcpSanitizeSegment reduces an untrusted string to something safe to use as a
// single path segment. Anything outside [A-Za-z0-9._-] becomes an underscore,
// leading dots are stripped (no ".." and no dotfiles that hide the result from
// the user), and the length is bounded. Empty input yields "file" so a caller
// can never end up joining an empty segment.
func mcpSanitizeSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 128 {
			break
		}
	}
	out := strings.TrimLeft(b.String(), ".")
	if out == "" {
		return "file"
	}
	return out
}

// mcpDownloadDest builds the local path a download writes to and creates its
// parent. Collisions get a -2, -3, ... suffix rather than overwriting: the LLM
// pulling the same filename from two hosts must not silently clobber the first
// copy, which in a forensics context would be destroying evidence.
func mcpDownloadDest(sessionID, remotePath string) (string, error) {
	dir := filepath.Join(mcpDownloadRoot(), mcpSanitizeSegment(sessionID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create download dir: %w", err)
	}
	base := mcpSanitizeSegment(path.Base(remotePath))
	dest := filepath.Join(dir, base)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 2; i < 1000; i++ {
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			return dest, nil
		}
		dest = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
	}
	return "", fmt.Errorf("too many copies of %s already downloaded", base)
}

// mcpFileSession resolves a session for the file tools: it must be shared at
// read-run (the same bar `run` sets, since these tools are the SFTP spelling of
// ls/cat) and it must be live.
//
// The error text names the ACTUAL requirement on purpose. An earlier wording
// ("session not shared for reading files") read as though a separate
// file-sharing permission existed, and an LLM that hit it told the user to go
// enable file access in the app - a setting that does not and will not exist.
// There is one axis: read, read-run, read-run-yolo.
// Every refusal is recorded to the activity ring. Without that, a tool the LLM
// never called and a tool the app refused look identical from the outside -
// both leave no trace - which makes "the LLM says it can't read files"
// impossible to diagnose. A refusal in the panel means the app said no; an
// empty panel means the model never asked.
func (a *App) mcpFileSession(sessionID, action string) (*sshlayer.Session, error) {
	lvl := a.grantLevel(sessionID)
	if !canRun(lvl) {
		reason := "shared read-only"
		if lvl == mcpGrantNone {
			// Far more likely than a genuine read-only share: a session id
			// that is not shared at all, e.g. a stale one from an earlier run.
			reason = "not shared with the LLM (unknown or stale session id)"
		}
		a.recordActivity(McpActivity{
			SessionID: sessionID, Session: a.sessionDisplayName(sessionID), Kind: "file",
			Command: action, Exit: "error", Gate: "denied",
			Output: "refused: session is " + reason,
		})
		return nil, fmt.Errorf("this session is %s. The file tools need the same level "+
			"as run: ask the user to share it as \"Read + run\" in the Share-with-LLM "+
			"popover. There is no separate file-sharing setting", reason)
	}
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		a.recordActivity(McpActivity{
			SessionID: sessionID, Session: a.sessionDisplayName(sessionID), Kind: "file",
			Command: action, Exit: "error", Gate: "denied",
			Output: "refused: session not connected",
		})
		return nil, fmt.Errorf("session not connected")
	}
	return sess, nil
}

// mcpHumanBytes formats a size for the approval prompt and tool output. The
// user decides whether to allow a download partly on how big it is, so this
// wants to be readable, not exact.
func mcpHumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// mcpListFiles returns a directory listing over SFTP. Auto-gated: this is the
// `ls` of the file tools and `ls` auto-runs under a read-run grant.
func (a *App) mcpListFiles(sessionID, remotePath string) (string, error) {
	sess, err := a.mcpFileSession(sessionID, "list_files "+remotePath)
	if err != nil {
		return "", err
	}
	name := a.sessionDisplayName(sessionID)
	resolved, entries, err := sess.SftpList(remotePath)
	if err != nil {
		a.recordActivity(McpActivity{
			SessionID: sessionID, Session: name, Kind: "file",
			Command: "list_files " + remotePath, Exit: "error", Gate: "auto",
		})
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d entries)\n", resolved, len(entries))
	shown := entries
	if len(shown) > mcpListFilesCap {
		shown = shown[:mcpListFilesCap]
	}
	for _, e := range shown {
		suffix := ""
		if e.IsDir {
			suffix = "/"
		} else if e.IsLink {
			suffix = " -> " + e.Target
		}
		fmt.Fprintf(&b, "%s  %10s  %s  %s%s\n",
			e.ModeStr, mcpHumanBytes(e.Size),
			time.Unix(e.ModTime, 0).Format("2006-01-02 15:04"),
			e.Name, suffix)
	}
	if len(entries) > len(shown) {
		fmt.Fprintf(&b, "...[%d more entries not shown]\n", len(entries)-len(shown))
	}
	out := b.String()
	a.recordActivity(McpActivity{
		SessionID: sessionID, Session: name, Kind: "file",
		Command: "list_files " + resolved, Output: out, Exit: "ok", Gate: "auto",
	})
	return out, nil
}

// mcpReadFile inlines a capped slice of a remote file. Auto-gated for the same
// reason as list_files: `run` with `cat` is already auto under read-run, so
// prompting here would only push the LLM back to the shell.
//
// Binary content comes back base64-encoded rather than as mangled UTF-8 - the
// point of this tool over `cat` is that file headers and small binaries survive
// the trip intact.
func (a *App) mcpReadFile(sessionID, remotePath string, maxBytes int) (string, error) {
	sess, err := a.mcpFileSession(sessionID, "read_file "+remotePath)
	if err != nil {
		return "", err
	}
	if maxBytes <= 0 {
		maxBytes = mcpReadFileDefaultCap
	}
	if maxBytes > mcpReadFileHardCap {
		maxBytes = mcpReadFileHardCap
	}
	name := a.sessionDisplayName(sessionID)
	cmd := fmt.Sprintf("read_file %s", remotePath)

	st, err := sess.SftpStat(remotePath)
	if err != nil {
		a.recordActivity(McpActivity{
			SessionID: sessionID, Session: name, Kind: "file",
			Command: cmd, Exit: "error", Gate: "auto",
		})
		return "", err
	}
	if st.IsDir {
		return "", fmt.Errorf("%s is a directory - use list_files", remotePath)
	}

	data, err := sess.SftpReadAll(remotePath, int64(maxBytes))
	if err != nil {
		a.recordActivity(McpActivity{
			SessionID: sessionID, Session: name, Kind: "file",
			Command: cmd, Exit: "error", Gate: "auto",
		})
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s", remotePath, mcpHumanBytes(st.Size))
	if int64(len(data)) < st.Size {
		fmt.Fprintf(&b, ", first %s shown - use download_file for the whole file",
			mcpHumanBytes(int64(len(data))))
	}
	b.WriteString(")\n")
	if utf8.Valid(data) {
		b.WriteString("--- BEGIN UNTRUSTED FILE CONTENT ---\n")
		b.Write(data)
		b.WriteString("\n--- END UNTRUSTED FILE CONTENT ---")
	} else {
		b.WriteString("binary content, base64:\n")
		b.WriteString(base64.StdEncoding.EncodeToString(data))
	}
	out := b.String()
	a.recordActivity(McpActivity{
		SessionID: sessionID, Session: name, Kind: "file",
		Command: cmd, Output: out, Exit: "ok", Gate: "auto",
	})
	return out, nil
}

// mcpDownloadFile streams a remote file into the download sandbox and returns
// the local path, so the caller can open it with its own filesystem tools
// instead of dragging the bytes through the context.
//
// Unlike the other two this always asks the user first (YOLO excepted): it is
// the only MCP tool that writes to local disk. The prompt names the size, which
// is why we stat before asking - "the LLM wants to pull 12 GB" is a decision
// the user should get to make.
//
// cancel is wired to the tool call's context, so an LLM client that gives up on
// a slow transfer actually stops it instead of leaving it streaming.
func (a *App) mcpDownloadFile(sessionID, remotePath string, cancel <-chan struct{}) (string, error) {
	lvl := a.grantLevel(sessionID)
	sess, err := a.mcpFileSession(sessionID, "download_file "+remotePath)
	if err != nil {
		return "", err
	}
	name := a.sessionDisplayName(sessionID)

	st, err := sess.SftpStat(remotePath)
	if err != nil {
		return "", err
	}
	if st.IsDir {
		return "", fmt.Errorf("%s is a directory - download_file takes a single file", remotePath)
	}

	prompt := fmt.Sprintf("download %s (%s) to the local mcp-downloads folder",
		remotePath, mcpHumanBytes(st.Size))
	gate := "yolo"
	if lvl != mcpGrantReadRunYolo {
		if a.requestApproval(sessionID, name, "file", prompt) == mcpDecisionDeny {
			a.recordActivity(McpActivity{
				SessionID: sessionID, Session: name, Kind: "file",
				Command: prompt, Gate: "denied",
			})
			return "", fmt.Errorf("download denied by user")
		}
		gate = "approved"
	}

	dest, err := mcpDownloadDest(sessionID, remotePath)
	if err != nil {
		return "", err
	}
	// Feed the same aggregate the UI transfers use, so an LLM pulling a large
	// file still fills the taskbar bar - this is exactly the case where the
	// user is not looking at the app and wants to know something is happening.
	transferID := uuid.NewString()
	written, err := sess.SftpDownload(remotePath, dest, func(done, total int64) {
		a.transferProgress(transferID, done, total)
	}, cancel)
	a.transferFinished(transferID, err != nil)
	if err != nil {
		// A cancelled or failed transfer leaves a partial file behind; drop it
		// so nothing downstream mistakes it for the real thing.
		_ = os.Remove(dest)
		a.recordActivity(McpActivity{
			SessionID: sessionID, Session: name, Kind: "file",
			Command: prompt, Exit: "error", Gate: gate,
		})
		return "", fmt.Errorf("download: %w", err)
	}

	out := fmt.Sprintf("downloaded %s (%s) to %s", remotePath, mcpHumanBytes(written), dest)
	a.recordActivity(McpActivity{
		SessionID: sessionID, Session: name, Kind: "file",
		Command: prompt, Output: out, Exit: "ok", Gate: gate,
	})
	return out, nil
}
