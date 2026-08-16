//go:build !windows

package update

import (
	"errors"
	"fmt"
	"os"
)

func replaceExecutable(current, candidate string) error {
	if err := os.Rename(candidate, current); err != nil {
		return fmt.Errorf("replace executable: %w", err)
	}
	return nil
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
