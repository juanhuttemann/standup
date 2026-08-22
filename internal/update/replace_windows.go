//go:build windows

package update

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

type windowsReplaceOps struct {
	rename         func(string, string) error
	remove         func(string) error
	scheduleDelete func(string) error
}

func replaceExecutable(current, candidate string) error {
	ops := windowsReplaceOps{
		rename: os.Rename,
		remove: os.Remove,
		scheduleDelete: func(path string) error {
			pathUTF16, err := windows.UTF16PtrFromString(path)
			if err != nil {
				return fmt.Errorf("encode backup path: %w", err)
			}
			if err := windows.MoveFileEx(pathUTF16, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT); err != nil {
				return fmt.Errorf("schedule backup deletion: %w", err)
			}
			return nil
		},
	}

	return replaceExecutableWindows(current, candidate, strconv.Itoa(os.Getpid()), ops)
}

func replaceExecutableWindows(current, candidate, suffix string, ops windowsReplaceOps) error {
	backup := current + ".old-" + suffix
	if err := ops.rename(current, backup); err != nil {
		return fmt.Errorf("back up current executable: %w", err)
	}

	if err := ops.rename(candidate, current); err != nil {
		replaceErr := fmt.Errorf("replace current executable: %w", err)
		if rollbackErr := ops.rename(backup, current); rollbackErr != nil {
			return errors.Join(replaceErr, fmt.Errorf("restore current executable: %w", rollbackErr))
		}
		return replaceErr
	}

	// A backup that cannot be deleted is not a failed update: the new binary
	// is already in place. Windows keeps a running executable locked, so the
	// process performing the update is precisely the one that cannot remove
	// its own backup, and the reboot-time fallback needs administrator rights
	// an ordinary install does not have. Both failing is the normal case, and
	// erroring there told users an update that had worked had failed. The
	// file is named .old-<pid>; the next run sweeps it.
	if err := ops.remove(backup); err != nil {
		_ = ops.scheduleDelete(backup) // best effort; needs elevation
	}

	return nil
}
