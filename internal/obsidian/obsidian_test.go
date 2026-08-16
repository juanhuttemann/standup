package obsidian

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishCreatesNoteAndDirectories(t *testing.T) {
	vault := t.TempDir()

	path, err := Publish(vault, "Standups/2026-08-16.md", "# Standup\n\n- shipped it\n")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(vault, "Standups", "2026-08-16.md"), path)
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
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
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
	for _, note := range []string{"", ".", "../outside.md", "nested/../../outside.md", filepath.Join(string(filepath.Separator), "outside.md")} {
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
