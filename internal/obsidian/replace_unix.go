//go:build !windows

package obsidian

import "os"

func replaceFile(from, to string) error {
	return os.Rename(from, to)
}
