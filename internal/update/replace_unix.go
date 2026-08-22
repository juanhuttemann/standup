//go:build !windows

package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Only Windows leaves a backup behind: a running executable is locked there.
// Everywhere else the rename is atomic and nothing is left to sweep.
func replaceExecutable(current, candidate string) (string, error) {
	if err := os.Rename(candidate, current); err != nil {
		return "", fmt.Errorf("replace executable: %w", err)
	}
	return "", nil
}

// sweepBackups removes stale <exe>.old-* files. It only runs on the
// up-to-date path of Run — where every such file is a leftover from a past
// Windows update and is safe to remove on any platform, even from a Wine
// setup that moved across OSes. Install guards its own call behind GOOS.
// Own-name exclusion matters only on Windows; nothing here is locked.
func sweepBackups(current string) {
	entries, err := os.ReadDir(filepath.Dir(current))
	if err != nil {
		return
	}
	prefix := filepath.Base(current) + ".old-"
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		_ = os.Remove(filepath.Join(filepath.Dir(current), entry.Name()))
	}
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}
