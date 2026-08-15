package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Task struct {
	ID        string    `json:"id"`
	Text      string    `json:"task"`
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
}

type Store struct {
	Path string
	Now  func() time.Time
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store: empty path")
	}
	return &Store{Path: path, Now: time.Now}, nil
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) load() ([]Task, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var t Task
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return nil, fmt.Errorf("store: parse line: %w", err)
		}
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Timestamp.Before(tasks[j].Timestamp) })
	return tasks, nil
}

func (s *Store) save(tasks []Task) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for _, t := range tasks {
		line, err := json.Marshal(t)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return os.WriteFile(s.Path, []byte(b.String()), 0o644)
}

func (s *Store) Add(text string) (Task, error) {
	if strings.TrimSpace(text) == "" {
		return Task{}, errors.New("store: empty task text")
	}
	t := Task{ID: uuid.NewString(), Text: text, Status: "todo", Timestamp: s.now()}
	tasks, err := s.load()
	if err != nil {
		return Task{}, err
	}
	tasks = append(tasks, t)
	return t, s.save(tasks)
}

func (s *Store) List() ([]Task, error) {
	tasks, err := s.load()
	if err != nil {
		return nil, err
	}
	if tasks == nil {
		tasks = []Task{}
	}
	return tasks, nil
}

func (s *Store) ListDay(day time.Time) ([]Task, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []Task
	for _, t := range all {
		if sameDay(t.Timestamp.In(day.Location()), day) {
			out = append(out, t)
		}
	}
	if out == nil {
		out = []Task{}
	}
	return out, nil
}

func (s *Store) SetStatus(id, status string) (Task, error) {
	if !validStatus(status) {
		return Task{}, fmt.Errorf("store: invalid status %q", status)
	}
	tasks, err := s.load()
	if err != nil {
		return Task{}, err
	}
	for i, t := range tasks {
		if t.ID == id {
			tasks[i].Status = status
			return tasks[i], s.save(tasks)
		}
	}
	return Task{}, fmt.Errorf("store: unknown id %q", id)
}

func (s *Store) Delete(id string) error {
	tasks, err := s.load()
	if err != nil {
		return err
	}
	for i, t := range tasks {
		if t.ID == id {
			return s.save(append(tasks[:i:i], tasks[i+1:]...))
		}
	}
	return fmt.Errorf("store: unknown id %q", id)
}

// FindByPrefix resolves a full id or a unique id prefix to its task.
func (s *Store) FindByPrefix(p string) (Task, error) {
	tasks, err := s.List()
	if err != nil {
		return Task{}, err
	}
	var matches []Task
	for _, t := range tasks {
		if t.ID == p || strings.HasPrefix(t.ID, p) {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Task{}, fmt.Errorf("store: no task with id %q", p)
	default:
		return Task{}, fmt.Errorf("store: ambiguous id %q (%d matches)", p, len(matches))
	}
}

func validStatus(s string) bool {
	return s == "todo" || s == "in-progress" || s == "done"
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
