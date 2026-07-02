// Package devinsession discovers the identity of the Devin CLI session a hook
// subprocess belongs to, by walking the process ancestry and looking for the
// session lock file the devin process holds open.
//
// Why: Devin's UserPromptSubmit hook payload carries only the prompt — no
// session id (unlike Claude Code's transcript_path or Codex's history.jsonl
// session_id). Locating the session by "working directory + most recent
// activity" silently binds to the WRONG conversation whenever several sessions
// run in the same directory (a real incident: a PPT-writing session's history
// was injected into an e2e-testing prompt). The hook process, however, is a
// child of the very devin process it belongs to, and every running devin
// session holds an open fd on <data-dir>/session_locks/<session-id>.lock —
// verified against live sessions, including devin-server remote ones. Walking
// /proc ancestry therefore yields the true session id with zero configuration
// and zero extra writes.
//
// Linux-only by design (/proc). On other platforms, or on any read failure,
// Discover returns "" and callers fall back to the heuristic lookup.
package devinsession

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// maxDepth bounds the ancestry walk. The expected chain is
// openpe -> sh -c -> devin (2-3 levels); 10 leaves generous headroom for
// wrapper shells without ever scanning unrelated ancestry like init.
const maxDepth = 10

// Discover walks the process ancestry starting at pid (inclusive) and returns
// the session id of the nearest ancestor holding an open
// <lockDir>/<session-id>.lock, or "" if none is found within maxDepth. All
// failures (missing /proc entries, unreadable fd tables, malformed status
// files) degrade to "" — identity discovery must never break enhancement.
func Discover(procRoot string, lockDir string, pid int) string {
	procRoot = strings.TrimSpace(procRoot)
	lockDir = filepath.Clean(strings.TrimSpace(lockDir))
	if procRoot == "" || lockDir == "" || lockDir == "." || pid <= 1 {
		return ""
	}
	for depth := 0; depth < maxDepth && pid > 1; depth++ {
		if id := lockedSessionID(procRoot, lockDir, pid); id != "" {
			return id
		}
		parent, ok := parentPID(procRoot, pid)
		if !ok {
			return ""
		}
		pid = parent
	}
	return ""
}

// lockedSessionID scans /proc/<pid>/fd for a symlink into lockDir and returns
// the lock file's base name without the .lock extension.
func lockedSessionID(procRoot string, lockDir string, pid int) string {
	fdDir := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return ""
	}
	prefix := lockDir + string(filepath.Separator)
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil {
			continue
		}
		if !strings.HasPrefix(target, prefix) || !strings.HasSuffix(target, ".lock") {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(target), ".lock")
		if id != "" {
			return id
		}
	}
	return ""
}

// parentPID reads the PPid field from /proc/<pid>/status.
func parentPID(procRoot string, pid int) (int, bool) {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "PPid:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "PPid:"))
		parent, err := strconv.Atoi(value)
		if err != nil || parent <= 0 {
			return 0, false
		}
		return parent, true
	}
	return 0, false
}

// DefaultLockDir derives the session_locks directory from the sessions DB
// path (they live side by side under the devin CLI data dir), so a DBPath
// override in config or tests relocates both consistently.
func DefaultLockDir(dbPath string) string {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), "session_locks")
}
