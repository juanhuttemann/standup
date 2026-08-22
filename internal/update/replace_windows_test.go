//go:build windows

package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplaceExecutableReplacesOrdinaryFile(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "standup.exe")
	candidate := filepath.Join(dir, "standup.exe.new")
	require.NoError(t, os.WriteFile(current, []byte("old"), 0o755))
	require.NoError(t, os.WriteFile(candidate, []byte("new"), 0o755))

	require.NoError(t, replaceExecutable(current, candidate))
	contents, err := os.ReadFile(current)
	require.NoError(t, err)
	assert.Equal(t, []byte("new"), contents)
	_, err = os.Stat(candidate)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestReplaceExecutableWindows(t *testing.T) {
	var renames [][2]string
	var removed string
	ops := windowsReplaceOps{
		rename: func(oldPath, newPath string) error {
			renames = append(renames, [2]string{oldPath, newPath})
			return nil
		},
		remove: func(path string) error {
			removed = path
			return nil
		},
		scheduleDelete: func(string) error {
			t.Fatal("scheduled deletion after successful removal")
			return nil
		},
	}

	require.NoError(t, replaceExecutableWindows(`C:\bin\standup.exe`, `C:\bin\standup.exe.new`, "42", ops))
	assert.Equal(t, [][2]string{
		{`C:\bin\standup.exe`, `C:\bin\standup.exe.old-42`},
		{`C:\bin\standup.exe.new`, `C:\bin\standup.exe`},
	}, renames)
	assert.Equal(t, `C:\bin\standup.exe.old-42`, removed)
}

func TestReplaceExecutableWindowsRollsBack(t *testing.T) {
	replaceErr := errors.New("replace failed")
	var renames [][2]string
	ops := windowsReplaceOps{
		rename: func(oldPath, newPath string) error {
			renames = append(renames, [2]string{oldPath, newPath})
			if oldPath == `C:\bin\standup.exe.new` {
				return replaceErr
			}
			return nil
		},
		remove: func(string) error {
			t.Fatal("removed backup after failed replacement")
			return nil
		},
	}

	err := replaceExecutableWindows(`C:\bin\standup.exe`, `C:\bin\standup.exe.new`, "42", ops)
	require.ErrorIs(t, err, replaceErr)
	assert.Equal(t, [][2]string{
		{`C:\bin\standup.exe`, `C:\bin\standup.exe.old-42`},
		{`C:\bin\standup.exe.new`, `C:\bin\standup.exe`},
		{`C:\bin\standup.exe.old-42`, `C:\bin\standup.exe`},
	}, renames)
}

func TestReplaceExecutableWindowsReportsRollbackFailure(t *testing.T) {
	replaceErr := errors.New("replace failed")
	rollbackErr := errors.New("rollback failed")
	ops := windowsReplaceOps{
		rename: func(oldPath, newPath string) error {
			switch oldPath {
			case `C:\bin\standup.exe`:
				return nil
			case `C:\bin\standup.exe.new`:
				return replaceErr
			default:
				return rollbackErr
			}
		},
	}

	err := replaceExecutableWindows(`C:\bin\standup.exe`, `C:\bin\standup.exe.new`, "42", ops)
	require.ErrorIs(t, err, replaceErr)
	assert.ErrorIs(t, err, rollbackErr)
}

func TestReplaceExecutableWindowsSchedulesLockedBackupDeletion(t *testing.T) {
	removeErr := errors.New("in use")
	var scheduled string
	ops := windowsReplaceOps{
		rename: func(string, string) error { return nil },
		remove: func(string) error { return removeErr },
		scheduleDelete: func(path string) error {
			scheduled = path
			return nil
		},
	}

	require.NoError(t, replaceExecutableWindows(`C:\bin\standup.exe`, `C:\bin\standup.exe.new`, "42", ops))
	assert.Equal(t, `C:\bin\standup.exe.old-42`, scheduled)
}

// Windows locks a running executable, so the backup usually cannot be
// deleted by the process that made it, and MOVEFILE_DELAY_UNTIL_REBOOT needs
// administrator rights an ordinary install does not have. Both failing is the
// normal case: the new binary is already in place, and reporting a failed
// update over one stale file told the user to distrust an update that worked.
func TestReplaceExecutableWindowsKeepsGoingWhenTheBackupCannotBeDeleted(t *testing.T) {
	ops := windowsReplaceOps{
		rename:         func(string, string) error { return nil },
		remove:         func(string) error { return errors.New("in use") },
		scheduleDelete: func(string) error { return errors.New("Access is denied") },
	}

	require.NoError(t,
		replaceExecutableWindows(`C:\bin\standup.exe`, `C:\bin\standup.exe.new`, "42", ops),
		"the replacement succeeded; only the cleanup did not")
}
