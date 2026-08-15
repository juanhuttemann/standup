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
	Subject string
	When    time.Time
}

// Log returns the configured git user's non-merge commits since the given
// time from the repository at dir (oldest first). Teammates' commits are
// excluded by matching the repository's user.email.
func Log(dir string, since time.Time) ([]Commit, error) {
	email, err := run(dir, "config", "user.email")
	if err != nil {
		return nil, err
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, errors.New("git: user.email not configured in repository")
	}
	out, err := run(dir, "log", "--no-merges",
		"--since="+since.Format(time.RFC3339),
		"--author="+email,
		"--pretty=format:%s%x1f%aI")
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		subject, when, ok := strings.Cut(line, "\x1f")
		if !ok {
			return nil, fmt.Errorf("git: unparseable log line %q", line)
		}
		ts, err := time.Parse(time.RFC3339, when)
		if err != nil {
			return nil, fmt.Errorf("git: commit time %q: %w", when, err)
		}
		commits = append(commits, Commit{Subject: subject, When: ts})
	}
	slices.Reverse(commits)
	return commits, nil
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
