//go:build !windows

package catalog

import "os"

func replaceCatalogFile(current, candidate string) error {
	return os.Rename(candidate, current)
}
