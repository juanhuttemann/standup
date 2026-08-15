package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const meEmail = "me@example.com"

// scratchRepo returns a git repo configured for me@example.com.
func scratchRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init")
	run("config", "user.name", "Me")
	run("config", "user.email", meEmail)
	return dir
}

// commitAt creates a commit with the given message, author and date.
func commitAt(t *testing.T, dir, message, authorEmail, date string) {
	t.Helper()
	file := filepath.Join(dir, fmt.Sprintf("f%d-%s", time.Now().UnixNano(), message))
	require.NoError(t, os.WriteFile(file, []byte(message), 0o644))
	cmd := exec.Command("git", "-C", dir, "add", file)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	cmd = exec.Command("git", "-C", dir, "commit", "-m", message)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Someone",
		"GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_NAME=Me",
		"GIT_COMMITTER_EMAIL="+meEmail,
		"GIT_COMMITTER_DATE="+date,
	)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestLogOneLinePerCommit(t *testing.T) {
	dir := scratchRepo(t)
	commitAt(t, dir, "fix login bug", meEmail, "2026-08-14T10:00:00+02:00")
	commitAt(t, dir, "write tests", meEmail, "2026-08-14T16:30:00+02:00")

	commits, err := Log(dir, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, commits, 2)
	assert.Equal(t, "fix login bug", commits[0].Subject)
	assert.Equal(t, "fix login bug", commits[0].Body)
	assert.NotEmpty(t, commits[0].Hash)
	want, err := time.Parse(time.RFC3339, "2026-08-14T10:00:00+02:00")
	require.NoError(t, err)
	assert.True(t, commits[0].When.Equal(want), "timestamp parsed per commit")
	assert.Equal(t, "write tests", commits[1].Subject)
}

func TestLogFullBodyWithoutTrailers(t *testing.T) {
	dir := scratchRepo(t)
	msg := "fix login bug\n\nThe token was expired.\n\nSigned-off-by: Me <me@example.com>\nCo-authored-by: Someone <someone@example.com>"
	commitAt(t, dir, msg, meEmail, "2026-08-14T10:00:00+02:00")

	commits, err := Log(dir, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, commits, 1)
	assert.Equal(t, "fix login bug", commits[0].Subject)
	assert.Equal(t, "fix login bug\n\nThe token was expired.", commits[0].Body,
		"full message kept, trailer block stripped")
}

func TestLogAllIncludesTeammates(t *testing.T) {
	dir := scratchRepo(t)
	commitAt(t, dir, "my work", meEmail, "2026-08-14T10:00:00+02:00")
	commitAt(t, dir, "teammate work", "teammate@example.com", "2026-08-14T11:00:00+02:00")
	since := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)

	mine, err := Log(dir, since)
	require.NoError(t, err)
	require.Len(t, mine, 1, "personal Log still filters teammates out")
	assert.Equal(t, meEmail, mine[0].Author, "commits carry their author email")

	all, err := LogAll(dir, since)
	require.NoError(t, err)
	require.Len(t, all, 2, "LogAll collects every author")
	assert.Equal(t, "teammate@example.com", all[1].Author)
}

func TestLogResolvesBranchNames(t *testing.T) {
	dir := scratchRepo(t)
	commitAt(t, dir, "first", meEmail, "2026-08-14T10:00:00+02:00")
	commitAt(t, dir, "second", meEmail, "2026-08-14T11:00:00+02:00")

	commits, err := Log(dir, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, commits, 2)
	def, err := run(dir, "branch", "--show-current")
	require.NoError(t, err)
	def = strings.TrimSpace(def)
	assert.Equal(t, def, commits[1].Branch, "tip commit names its branch")
	assert.True(t, strings.HasPrefix(commits[0].Branch, def), "older commits name the nearest branch (name~N), got %q", commits[0].Branch)
}

func TestSubmodules(t *testing.T) {
	parent := scratchRepo(t)
	// A submodule source repo with one commit, added as lib/.
	sub := scratchRepo(t)
	commitAt(t, sub, "lib init", meEmail, "2026-08-14T09:00:00+02:00")
	cmd := exec.Command("git", "-C", parent, "-c", "protocol.file.allow=always",
		"submodule", "add", sub, "lib")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	subs, err := Submodules(parent)
	require.NoError(t, err)
	assert.Equal(t, []string{"lib"}, subs)
}

func TestSubmodulesNoneWithoutGitmodules(t *testing.T) {
	dir := scratchRepo(t)
	subs, err := Submodules(dir)
	require.NoError(t, err)
	assert.Empty(t, subs, "no .gitmodules, no submodules, no error")
}

func TestLogConventionalCommitSubjectsKept(t *testing.T) {
	dir := scratchRepo(t)
	commitAt(t, dir, "chore: seed repo", meEmail, "2026-08-14T10:00:00+02:00")
	commitAt(t, dir, "feat: add login", meEmail, "2026-08-14T11:00:00+02:00")

	commits, err := Log(dir, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, commits, 2)
	assert.Equal(t, "chore: seed repo", commits[0].Subject, "single-line conventional subject kept verbatim")
	assert.Equal(t, "chore: seed repo", commits[0].Body)
	assert.Equal(t, "feat: add login", commits[1].Subject)
}

func TestLogConventionalSubjectWithTrailerBlock(t *testing.T) {
	dir := scratchRepo(t)
	msg := "fix: login\n\nSigned-off-by: Me <me@example.com>"
	commitAt(t, dir, msg, meEmail, "2026-08-14T10:00:00+02:00")

	commits, err := Log(dir, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, commits, 1)
	assert.Equal(t, msg, commits[0].Body, "stripping to nothing keeps the whole message verbatim")
}

func TestLogCoAuthored(t *testing.T) {
	dir := scratchRepo(t)
	commitAt(t, dir, "teammate feature\n\nCo-authored-by: me <me@example.com>", "teammate@example.com", "2026-08-14T11:00:00+02:00")
	commitAt(t, dir, "other teammate work", "teammate@example.com", "2026-08-14T12:00:00+02:00")

	commits, err := Log(dir, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, commits, 1, "co-authored commits collected, plain teammate work excluded")
	assert.Equal(t, "teammate feature", commits[0].Subject)
	assert.Equal(t, "teammate feature", commits[0].Body, "co-authored trailer stripped from the task text")
}

func TestLogAuthorFilter(t *testing.T) {
	dir := scratchRepo(t)
	commitAt(t, dir, "mine", meEmail, "2026-08-14T10:00:00+02:00")
	commitAt(t, dir, "theirs", "teammate@example.com", "2026-08-14T11:00:00+02:00")

	commits, err := Log(dir, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, commits, 1, "teammates' commits excluded")
	assert.Equal(t, "mine", commits[0].Subject)
}

func TestLogLookbackBoundary(t *testing.T) {
	dir := scratchRepo(t)
	commitAt(t, dir, "old", meEmail, "2026-08-12T10:00:00+02:00")
	commitAt(t, dir, "recent", meEmail, "2026-08-14T10:00:00+02:00")

	commits, err := Log(dir, time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, commits, 1, "commits before the window excluded")
	assert.Equal(t, "recent", commits[0].Subject)
}

func TestLogMergeCommitsExcluded(t *testing.T) {
	dir := scratchRepo(t)
	commitAt(t, dir, "base", meEmail, "2026-08-14T09:00:00+02:00")
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("checkout", "-q", "-b", "feature")
	commitAt(t, dir, "feature work", meEmail, "2026-08-14T10:00:00+02:00")
	run("checkout", "-q", "-")
	run("merge", "--no-ff", "-m", "merge feature", "feature")

	commits, err := Log(dir, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Len(t, commits, 2)
	for _, c := range commits {
		assert.NotEqual(t, "merge feature", c.Subject, "merge commits excluded")
	}
}

func TestLogNotARepo(t *testing.T) {
	_, err := Log(t.TempDir(), time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not inside a git working tree",
		"repo check runs before the identity check, so the diagnosis is right")
}

func TestLogNoUserEmailSuggestsFix(t *testing.T) {
	// Isolate from the developer's own global config so user.email is unset.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init")

	_, err := Log(dir, time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "git config --global user.email", "error names the exact fix")
}
