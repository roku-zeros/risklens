package lens

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const lockFormat = "risklens-os-lock-v1"

func acquireCacheLock(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("cache lock must be a regular non-symlink file")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			f.Close()
		}
	}()
	if err = lockCacheFile(f); err != nil {
		return nil, fmt.Errorf("cache already owned by a live process (OS lock): %w; close that MCP connection; do not delete server.lock", err)
	}
	info, err := os.Lstat(path)
	actual, statErr := f.Stat()
	if err != nil || statErr != nil || !info.Mode().IsRegular() || !os.SameFile(info, actual) {
		return nil, fmt.Errorf("cache lock path changed during acquisition")
	}
	b := make([]byte, 256)
	n, _ := f.ReadAt(b, 0)
	text := strings.TrimSpace(string(b[:n]))
	parts := strings.Split(text, "\n")
	// Old releases used a PID marker without an OS lock. Do not overlap with one.
	if text != "" && (len(parts) < 2 || parts[1] != lockFormat) {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(parts[0]))
		if parseErr != nil || pid <= 0 {
			return nil, fmt.Errorf("unrecognized legacy server.lock; inspect the owner before migrating")
		}
		alive, probeErr := legacyProcessAlive(pid)
		if probeErr != nil || alive {
			return nil, fmt.Errorf("legacy RiskLens lock PID %d is alive or cannot be checked; stop the old server before upgrading", pid)
		}
	}
	if err = f.Truncate(0); err != nil {
		return nil, err
	}
	if _, err = f.Seek(0, 0); err != nil {
		return nil, err
	}
	if _, err = fmt.Fprintf(f, "%d\n%s\n", os.Getpid(), lockFormat); err != nil {
		return nil, err
	}
	failed = false
	return f, nil
}
