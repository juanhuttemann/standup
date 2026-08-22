package obsidian

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishCreatesNoteAndDirectories(t *testing.T) {
	vault := t.TempDir()

	path, err := Publish(vault, "Standups/2026-08-16.md", "# Standup\n\n- shipped it\n")
	require.NoError(t, err)
	// Publish canonicalizes the vault, which on Windows also expands 8.3
	// short names (RUNNER~1 -> runneradmin); compare the note path itself.
	assert.True(t, strings.HasSuffix(path, filepath.Join("Standups", "2026-08-16.md")),
		"note lands below the vault, got %s", path)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "<!-- standup:start -->\n# Standup\n\n- shipped it\n<!-- standup:end -->\n", string(contents))
}

func TestPublishReplacesManagedBlockAndPreservesRest(t *testing.T) {
	vault := t.TempDir()
	path := filepath.Join(vault, "daily.md")
	original := "# Daily\n\nBefore\n<!-- standup:start -->\nold\n<!-- standup:end -->\nAfter\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o640))

	_, err := Publish(vault, "daily.md", "new\n")
	require.NoError(t, err)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "# Daily\n\nBefore\n<!-- standup:start -->\nnew\n<!-- standup:end -->\nAfter\n", string(contents))
	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		// Windows has no Unix permission bits; every file reads as 0666.
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	}
}

func TestPublishAppendsBlockWhenMarkersAreAbsent(t *testing.T) {
	vault := t.TempDir()
	path := filepath.Join(vault, "daily.md")
	require.NoError(t, os.WriteFile(path, []byte("# Daily\n\nNotes"), 0o600))

	_, err := Publish(vault, "daily.md", "report")
	require.NoError(t, err)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "# Daily\n\nNotes\n\n<!-- standup:start -->\nreport\n<!-- standup:end -->\n", string(contents))
}

func TestPublishRejectsUnsafePaths(t *testing.T) {
	vault := t.TempDir()
	unsafe := []string{"", ".", "../outside.md", "nested/../../outside.md"}
	// A rooted path is rejected as absolute on Unix; on Windows "\outside.md"
	// is volume-relative, so use a drive letter for the same guarantee.
	if runtime.GOOS == "windows" {
		unsafe = append(unsafe, `C:\outside.md`)
	} else {
		unsafe = append(unsafe, "/outside.md")
	}
	for _, note := range unsafe {
		t.Run(note, func(t *testing.T) {
			_, err := Publish(vault, note, "report")
			require.Error(t, err)
		})
	}
}

func TestPublishRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require privileges on Windows")
	}
	vault := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(vault, "escape")))

	_, err := Publish(vault, "escape/note.md", "report")
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(outside, "note.md"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestPublishMalformedMarkersLeaveNoteUnchanged(t *testing.T) {
	tests := []string{
		"before\n<!-- standup:start -->\nold\n",
		"before\n<!-- standup:end -->\nafter\n",
		"<!-- standup:start -->\none\n<!-- standup:end -->\n<!-- standup:start -->\ntwo\n<!-- standup:end -->\n",
		"<!-- standup:end -->\n<!-- standup:start -->\n",
	}
	for _, original := range tests {
		t.Run(original, func(t *testing.T) {
			vault := t.TempDir()
			path := filepath.Join(vault, "daily.md")
			require.NoError(t, os.WriteFile(path, []byte(original), 0o600))

			_, err := Publish(vault, "daily.md", "new")
			require.Error(t, err)
			contents, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, original, string(contents))
		})
	}
}

func TestPublishRejectsMarkersInReport(t *testing.T) {
	vault := t.TempDir()
	path := filepath.Join(vault, "daily.md")
	original := "# Daily\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o600))

	_, err := Publish(vault, "daily.md", "task text <!-- standup:end -->")
	require.Error(t, err)
	contents, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, original, string(contents))
}
