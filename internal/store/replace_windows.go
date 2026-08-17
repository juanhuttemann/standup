//go:build windows

package store

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func replaceStoreFile(current, candidate string) error {
	currentUTF16, err := windows.UTF16PtrFromString(current)
	if err != nil {
		return fmt.Errorf("encode store path: %w", err)
	}
	candidateUTF16, err := windows.UTF16PtrFromString(candidate)
	if err != nil {
		return fmt.Errorf("encode temporary store path: %w", err)
	}
	if err := windows.MoveFileEx(
		candidateUTF16,
		currentUTF16,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	); err != nil {
		return fmt.Errorf("replace store: %w", err)
	}
	return nil
}
