// Package git collects the repository's own commits as standup input.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

type Commit struct {
	Hash    string
	Subject string
	Body    string // full commit message, trailer block stripped
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
	if _, err := run(dir, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, errNotARepo
	}
	email, err := Identity(dir)
	if err != nil {
		return nil, err
	}
	out, err := run(dir, "log", "--no-merges",
		"--since="+since.Format(time.RFC3339),
		"--pretty=format:%H%x1f%aI%x1f%ae%x1f%B")
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, e := range parseLog(out) {
		if !strings.EqualFold(e.author, email) && !coAuthored(e.raw, email) {
			continue
		}
		ts, err := time.Parse(time.RFC3339, e.when)
		if err != nil {
			return nil, fmt.Errorf("git: commit time %q: %w", e.when, err)
		}
		subject, body := cleanMessage(e.raw)
		commits = append(commits, Commit{Hash: e.hash, Subject: subject, Body: body, When: ts})
	}
	slices.Reverse(commits)
	return commits, nil
}

// entry is one raw log record; body lines are reattached by parseLog.
type entry struct {
	hash, when, author, raw string
}

// parseLog splits the \x1f-fielded, newline-separated log output. A new
// record starts at a line beginning with a 40-hex commit hash.
// ponytail: body lines containing \x1f would merge into the previous record;
// real commit messages don't.
func parseLog(out string) []entry {
	var entries []entry
	for _, line := range strings.Split(out, "\n") {
		if !isHashLine(line) {
			if len(entries) > 0 {
				entries[len(entries)-1].raw += "\n" + line
			}
			continue
		}
		parts := strings.SplitN(line, "\x1f", 4)
		entries = append(entries, entry{hash: parts[0], when: parts[1], author: parts[2], raw: parts[3]})
	}
	return entries
}

func isHashLine(line string) bool {
	if len(line) < 41 || line[40] != '\x1f' {
		return false
	}
	for _, r := range line[:40] {
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
