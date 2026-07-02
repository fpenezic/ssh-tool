package recorder

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileInfo describes one .cast file for the recordings browser.
// Duration is best-effort (timestamp of the last event line); 0 for
// an empty or just-started recording.
type FileInfo struct {
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	Size     int64   `json:"size"`
	ModTime  int64   `json:"mod_time"`
	Title    string  `json:"title"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Duration float64 `json:"duration"`
}

// ReadInfo parses the header line plus the tail of the file (for the
// last event timestamp) without loading the whole recording.
func ReadInfo(path string) (FileInfo, error) {
	st, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	info := FileInfo{
		Name:    filepath.Base(path),
		Path:    path,
		Size:    st.Size(),
		ModTime: st.ModTime().Unix(),
	}

	f, err := os.Open(path)
	if err != nil {
		return FileInfo{}, err
	}
	defer f.Close()

	// Header is the first line and always small.
	br := bufio.NewReader(f)
	headerLine, err := br.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return FileInfo{}, err
	}
	var h header
	if err := json.Unmarshal(headerLine, &h); err == nil {
		info.Title = h.Title
		info.Width = h.Width
		info.Height = h.Height
	}

	// Duration: read a tail window and take the timestamp of the last
	// complete event line. A single event line larger than the window
	// only happens for multi-hundred-KB output bursts at the very end;
	// duration degrades to 0 there, which the UI tolerates.
	const tailWindow = 256 * 1024
	off := st.Size() - tailWindow
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return info, nil
	}
	lines := strings.Split(string(buf), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || !strings.HasPrefix(line, "[") {
			continue
		}
		var ev []any
		if err := json.Unmarshal([]byte(line), &ev); err != nil || len(ev) < 1 {
			continue
		}
		if t, ok := ev[0].(float64); ok {
			info.Duration = t
			break
		}
	}
	return info, nil
}

// ListDir returns FileInfo for every .cast file in dir, newest first.
// A missing dir is an empty list, not an error (nothing recorded yet).
func ListDir(dir string) ([]FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []FileInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".cast") {
			continue
		}
		info, err := ReadInfo(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // unreadable file - skip rather than break the list
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime > out[j].ModTime })
	return out, nil
}
