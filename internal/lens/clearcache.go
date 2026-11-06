package lens

import (
	"os"
	"path/filepath"
	"strings"
)

// ClearCache holds the same OS lock as the server. The stable lock inode is never removed.
func ClearCache(c Config) ([]string, error) {
	removed := []string{}
	if err := c.Defaults(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.Cache, 0700); err != nil {
		return nil, err
	}
	f, err := acquireCacheLock(filepath.Join(c.Cache, "server.lock"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := os.ReadDir(c.Cache)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "vectors.json" && !(strings.HasPrefix(name, "vectors-") && strings.HasSuffix(name, ".tmp")) {
			continue
		}
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(c.Cache, name)
		if err = os.Remove(path); err != nil {
			return removed, err
		}
		removed = append(removed, path)
	}
	return removed, nil
}
