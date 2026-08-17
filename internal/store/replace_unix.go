//go:build !windows

package store

import "os"

func replaceStoreFile(current, candidate string) error {
	return os.Rename(candidate, current)
}
