//go:build windows

package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

	_, err := replaceExecutable(current, candidate)
	require.NoError(t, err)
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

	leftover, err := replaceExecutableWindows(`C:\bin\standup.exe`, `C:\bin\standup.exe.new`, "42", ops)
	require.NoError(t, err)
	assert.Empty(t, leftover, "a backup that was deleted is not leftover")
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

	_, err := replaceExecutableWindows(`C:\bin\standup.exe`, `C:\bin\standup.exe.new`, "42", ops)
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

	_, err := replaceExecutableWindows(`C:\bin\standup.exe`, `C:\bin\standup.exe.new`, "42", ops)
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

	leftover, err := replaceExecutableWindows(`C:\bin\standup.exe`, `C:\bin\standup.exe.new`, "42", ops)
	require.NoError(t, err)
	assert.Equal(t, `C:\bin\standup.exe.old-42`, leftover)
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

	leftover, err := replaceExecutableWindows(`C:\bin\standup.exe`, `C:\bin\standup.exe.new`, "42", ops)
	require.NoError(t, err, "the replacement succeeded; only the cleanup did not")
	assert.Equal(t, `C:\bin\standup.exe.old-42`, leftover,
		"the exact leftover is reported, not the first glob match")
}

// A recycled pid means <exe>.old-<pid> already exists, and renaming onto a
// locked file fails with "Access is denied". The retry with a nanosecond
// suffix keeps that from aborting the update.
func TestReplaceExecutableWindowsRetriesOnPidReuse(t *testing.T) {
	removeErr := errors.New("in use")
	var renames [][2]string
	var scheduled []string
	ops := windowsReplaceOps{
		rename: func(oldPath, newPath string) error {
			renames = append(renames, [2]string{oldPath, newPath})
			if oldPath == `C:\bin\standup.exe` && newPath == `C:\bin\standup.exe.old-42` {
				return errors.New("Access is denied")
			}
			return nil
		},
		remove: func(string) error { return removeErr },
		scheduleDelete: func(path string) error {
			scheduled = append(scheduled, path)
			return nil
		},
	}

	leftover, err := replaceExecutableWindows(`C:\bin\standup.exe`, `C:\bin\standup.exe.new`, "42", ops)
	require.NoError(t, err)
	require.Len(t, renames, 3, "first backup attempt fails, retry uses a fresh suffix")
	assert.Equal(t, [2]string{`C:\bin\standup.exe`, `C:\bin\standup.exe.old-42`}, renames[0])
	assert.Equal(t, `C:\bin\standup.exe`, renames[1][0])
	assert.NotEqual(t, `C:\bin\standup.exe.old-42`, renames[1][1], "the retry picks a new name")
	assert.True(t, strings.HasPrefix(renames[1][1], `C:\bin\standup.exe.old-42-`),
		"the retry suffix extends the pid, got %s", renames[1][1])
	assert.Equal(t, renames[1][1], leftover)
	assert.Equal(t, []string{leftover}, scheduled)
}

// The sweep removes earlier backups and tries this run's own too: on a real
// install that file is locked by the running process and the Remove fails,
// which is the Windows cleanup model. A test machine locks nothing, so the
// file is gone — what matters is that the stale backup is swept and the
// executable itself is untouched.
func TestSweepBackupsOnWindows(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "standup.exe")
	require.NoError(t, os.WriteFile(current, []byte("new"), 0o755))
	stale := current + ".old-1"
	require.NoError(t, os.WriteFile(stale, []byte("stale"), 0o644))
	own := current + ".old-" + backupSuffix()
	require.NoError(t, os.WriteFile(own, []byte("own"), 0o644))

	sweepBackups(current)
	_, err := os.Stat(stale)
	assert.ErrorIs(t, err, os.ErrNotExist, "an earlier run's backup is gone")
	_, err = os.Stat(current)
	require.NoError(t, err, "the executable itself is never swept")
}
