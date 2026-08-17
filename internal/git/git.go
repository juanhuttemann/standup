// Package git collects the repository's own commits as standup input.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Commit struct {
	Hash    string
	Subject string
	Body    string // full commit message, trailer block stripped
	Author  string // author email
	Branch  string // nearest branch name (name~N for ancestors), best-effort
	When    time.Time
}

var (
	errNotARepo = errors.New("git: not inside a git working tree — run inside a repository")
	errNoEmail  = errors.New("git: user.email is not configured — set it with:\n  git config --global user.email you@example.com")
)

// Identity returns the configured git user email for dir.
func Identity(dir string) (string, error) {
	email, err := run(dir, "config", "user.email")
	if err != nil {
		return "", errNoEmail
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return "", errNoEmail
	}
	return email, nil
}

// Log returns the user's non-merge commits since the given time from the
// repository at dir (oldest first). A commit counts when it was authored by
// the configured git user or carries their address in a Co-authored-by
// trailer; teammates' commits are excluded.
func Log(dir string, since time.Time) ([]Commit, error) {
	return logCommits(dir, since, false)
}

// LogAll is Log without the identity filter: every author's non-merge
// commits, so one person can run the standup for the whole team.
func LogAll(dir string, since time.Time) ([]Commit, error) {
	return logCommits(dir, since, true)
}

func logCommits(dir string, since time.Time, allAuthors bool) ([]Commit, error) {
	if _, err := run(dir, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, errNotARepo
	}
	var email string
	if !allAuthors {
		var err error
		email, err = Identity(dir)
		if err != nil {
			return nil, err
		}
	}
	out, err := run(dir, "log", "--no-merges",
		"--since="+since.Format(time.RFC3339),
		"--pretty=format:%H%x00%aI%x00%ae%x00%B%x00")
	if err != nil {
		return nil, err
	}
	var commits []Commit
	entries, err := parseLog(out)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !allAuthors && !strings.EqualFold(e.author, email) && !coAuthored(e.raw, email) {
			continue
		}
		ts, err := time.Parse(time.RFC3339, e.when)
		if err != nil {
			return nil, fmt.Errorf("git: commit time %q: %w", e.when, err)
		}
		subject, body := cleanMessage(e.raw)
		commits = append(commits, Commit{Hash: e.hash, Subject: subject, Body: body, Author: e.author, When: ts})
	}
	slices.Reverse(commits)
	names := nameRev(dir, commits)
	for i := range commits {
		commits[i].Branch = names[commits[i].Hash]
	}
	return commits, nil
}

// nameRev resolves a branch name per commit via one git name-rev call
// (ancestors of a tip come out as name~N). Best-effort by design: a repo
// name-rev chokes on must not lose the commits, so a failure yields no
// names at all.
func nameRev(dir string, commits []Commit) map[string]string {
	if len(commits) == 0 {
		return nil
	}
	hashes := make([]string, len(commits))
	for i, c := range commits {
		hashes[i] = c.Hash
	}
	out, err := run(dir, append([]string{"name-rev", "--name-only"}, hashes...)...)
	if err != nil {
		return nil
	}
	names := map[string]string{}
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if i < len(hashes) {
			names[hashes[i]] = line
		}
	}
	return names
}

// entry is one raw log record.
type entry struct {
	hash, when, author, raw string
}

// parseLog splits NUL-delimited fields. Git commit messages cannot contain
// NUL bytes, so message contents cannot be mistaken for record boundaries.
func parseLog(out string) ([]entry, error) {
	if out == "" {
		return nil, nil
	}
	fields := strings.Split(out, "\x00")
	if fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%4 != 0 {
		return nil, fmt.Errorf("git: malformed log output: got %d fields", len(fields))
	}
	entries := make([]entry, 0, len(fields)/4)
	for i := 0; i < len(fields); i += 4 {
		hash := strings.TrimLeft(fields[i], "\r\n")
		if !isObjectID(hash) {
			return nil, fmt.Errorf("git: malformed object id %q", hash)
		}
		entries = append(entries, entry{hash: hash, when: fields[i+1], author: fields[i+2], raw: fields[i+3]})
	}
	return entries, nil
}

func isObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// coAuthored reports whether the raw message carries the email in a
// Co-authored-by trailer (case-insensitive, git treats trailers so).
func coAuthored(raw, email string) bool {
	want := strings.ToLower(email)
	for _, line := range strings.Split(raw, "\n") {
		l := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(l, "co-authored-by:") && strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// cleanMessage strips the trailing trailer block (Signed-off-by,
// Co-authored-by, …) and returns the subject line plus the full message.
func cleanMessage(raw string) (subject, body string) {
	lines := strings.Split(raw, "\n")
	end := len(lines)
	for end > 0 {
		l := strings.TrimSpace(lines[end-1])
		if l == "" {
			end--
			continue
		}
		if !isTrailer(l) {
			break
		}
		end--
	}
	// A strip that would empty the message means the whole text looked like
	// trailers (a conventional-commit subject such as "chore: seed repo") —
	// keep it verbatim; only a block leaving at least one line behind is
	// treated as trailers.
	if end == 0 {
		end = len(lines)
	}
	body = strings.TrimSpace(strings.Join(lines[:end], "\n"))
	subject = strings.SplitN(body, "\n", 2)[0]
	return subject, body
}

// ponytail: tail-of-message "Token: value" lines are treated as trailers; a
// prose last line like "docs: see here" would be stripped too.
func isTrailer(line string) bool {
	i := strings.Index(line, ":")
	if i <= 0 {
		return false
	}
	for _, r := range line[:i] {
		isAlpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !isAlpha && r != '-' {
			return false
		}
	}
	return i+1 < len(line) && line[i+1] == ' '
}

// Submodules returns the working-tree paths of the repository's submodules
// (relative to dir, per .gitmodules); nil when there are none.
func Submodules(dir string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(dir, ".gitmodules")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out, err := run(dir, "config", "-f", ".gitmodules", "--get-regexp", `submodule\..*\.path`)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil // a .gitmodules with no submodule sections
		}
		return nil, err
	}
	var subs []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		// "submodule.<name>.path <value>"
		if f := strings.Fields(line); len(f) == 2 {
			subs = append(subs, f[1])
		}
	}
	return subs, nil
}

func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
