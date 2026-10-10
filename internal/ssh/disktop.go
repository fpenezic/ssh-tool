package ssh

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// DirUsage is one directory in the "largest directories" list of a
// filesystem, size in 1024-byte blocks.
type DirUsage struct {
	Path   string `json:"path"`
	SizeKB int64  `json:"size_kb"`
}

// DiskTopResult is what DiskTopDirs found. Partial is set when du was cut
// off by the timeout or could not read some directories (permission), so
// the list is a lower bound rather than the whole picture.
type DiskTopResult struct {
	Dirs    []DirUsage `json:"dirs"`
	Partial bool       `json:"partial"`
	// TimedOut: du hit diskTopTimeoutSec, so big trees are missing, not
	// just the unreadable ones.
	TimedOut bool   `json:"timed_out"`
	Reason   string `json:"reason"`
}

// diskTopTimeoutSec caps du on the remote side. du walks every inode, so a
// slow disk or a tree of millions of small files can take minutes; this
// only ever runs on an explicit click and never on the stats timer.
const diskTopTimeoutSec = 20

const diskTopLimit = 15

// DiskTopDirs lists the largest directories (two levels deep) on the
// filesystem mounted at mount. -x keeps du on that one filesystem, nice
// keeps it out of the way of real work, timeout bounds it. Sorting happens
// here, not in a remote pipeline, so the exit status of du itself survives
// and a timeout can be told apart from a permission error.
func DiskTopDirs(client *ssh.Client, mount string) (*DiskTopResult, error) {
	if client == nil {
		return nil, fmt.Errorf("no live ssh client")
	}
	if !strings.HasPrefix(mount, "/") {
		return nil, fmt.Errorf("mount must be an absolute path")
	}
	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("du session: %w", err)
	}
	defer sess.Close()
	cmd := fmt.Sprintf("timeout %d nice -n 19 du -xk -d 2 -- %s 2>/dev/null; echo __SSHTOOL_RC__$?",
		diskTopTimeoutSec, quoteAlways(mount))
	out, _ := sess.Output(cmd)
	return parseDiskTop(string(out), mount)
}

func parseDiskTop(out, mount string) (*DiskTopResult, error) {
	res := &DiskTopResult{}
	rc := -1
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, "__SSHTOOL_RC__"); ok {
			rc, _ = strconv.Atoi(strings.TrimSpace(v))
			continue
		}
		size, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		kb, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
		if err != nil || path == mount {
			continue // the mount itself is the total, not a directory in it
		}
		res.Dirs = append(res.Dirs, DirUsage{Path: path, SizeKB: kb})
	}
	switch rc {
	case 0:
	case 124:
		res.Partial, res.TimedOut, res.Reason = true, true, fmt.Sprintf("stopped after %ds - sizes are a lower bound", diskTopTimeoutSec)
	case 127:
		return nil, fmt.Errorf("du or timeout is not available on this host")
	default:
		if len(res.Dirs) == 0 {
			return nil, fmt.Errorf("du failed (exit %d)", rc)
		}
		res.Partial, res.Reason = true, "some directories were not readable - sizes are a lower bound"
	}
	sort.Slice(res.Dirs, func(i, j int) bool { return res.Dirs[i].SizeKB > res.Dirs[j].SizeKB })
	if len(res.Dirs) > diskTopLimit {
		res.Dirs = res.Dirs[:diskTopLimit]
	}
	return res, nil
}

// quoteAlways single-quotes s unconditionally: a mount path comes from the
// remote df output, so it is quoted even when it looks like a plain word.
func quoteAlways(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
