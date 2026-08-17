package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Task struct {
	ID        string    `json:"id"`
	Text      string    `json:"task"`
	Status    string    `json:"status"`
	Author    string    `json:"author,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type Store struct {
	Path string
	Now  func() time.Time

	mu      sync.Mutex
	replace func(string, string) error
}

type OperationKind string

const (
	OperationCreate OperationKind = "create"
	OperationEdit   OperationKind = "edit"
	OperationStatus OperationKind = "status"
	OperationDelete OperationKind = "delete"
)

// BatchOperation describes one deterministic store mutation. Timestamp only
// applies to creates; a zero timestamp uses the store clock.
type BatchOperation struct {
	Kind      OperationKind
	ID        string
	Text      string
	Status    string
	Timestamp time.Time
}

// Change records the persisted before and after state of one operation.
// Creates have no Before value and deletes have no After value.
type Change struct {
	Kind   OperationKind
	Before *Task
	After  *Task
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
	ids := make(map[string]struct{})
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var t Task
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return nil, fmt.Errorf("store: parse line %d: %w", i+1, err)
		}
		if err := validateTask(t); err != nil {
			return nil, fmt.Errorf("store: line %d: %w", i+1, err)
		}
		if _, exists := ids[t.ID]; exists {
			return nil, fmt.Errorf("store: duplicate id %q", t.ID)
		}
		ids[t.ID] = struct{}{}
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Timestamp.Before(tasks[j].Timestamp) })
	return tasks, nil
}

func validateTask(t Task) error {
	if strings.TrimSpace(t.ID) == "" {
		return errors.New("empty task id")
	}
	if strings.TrimSpace(t.Text) == "" {
		return errors.New("empty task text")
	}
	if !validStatus(t.Status) {
		return errInvalidStatus(t.Status)
	}
	if t.Timestamp.IsZero() {
		return errors.New("zero task timestamp")
	}
	return nil
}

func (s *Store) save(tasks []Task) (err error) {
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
	dir := filepath.Dir(s.Path)
	mode := os.FileMode(0o644)
	info, statErr := os.Stat(s.Path)
	if statErr == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(s.Path)+"-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary store: %w", removeErr))
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if _, err := temporary.WriteString(b.String()); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err := temporary.Sync(); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	replace := s.replace
	if replace == nil {
		replace = replaceStoreFile
	}
	return replace(s.Path, temporaryPath)
}

func (s *Store) Add(text string) (Task, error) {
	return s.AddWithStatus(text, "todo")
}

// AddWithStatus adds a task with an explicit status; an empty status defaults
// to todo. Status is validated at this boundary.
func (s *Store) AddWithStatus(text, status string) (Task, error) {
	return s.AddAt(text, status, s.now())
}

// AddAt adds a task with an explicit status and timestamp (imports stamp the
// event time, not the import time).
func (s *Store) AddAt(text, status string, ts time.Time) (Task, error) {
	if status == "" {
		status = "todo"
	}
	if !validStatus(status) {
		return Task{}, errInvalidStatus(status)
	}
	if strings.TrimSpace(text) == "" {
		return Task{}, errors.New("store: empty task text")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := Task{ID: uuid.NewString(), Text: text, Status: status, Timestamp: ts}
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

// ListRange returns tasks whose timestamp falls within the calendar days of
// from and to (inclusive), interpreted in the location of from.
func (s *Store) ListRange(from, to time.Time) ([]Task, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	lo, hi := dayNum(from), dayNum(to)
	if lo > hi {
		lo, hi = hi, lo
	}
	var out []Task
	for _, t := range all {
		if n := dayNum(t.Timestamp.In(from.Location())); n >= lo && n <= hi {
			out = append(out, t)
		}
	}
	if out == nil {
		out = []Task{}
	}
	return out, nil
}

// UpdateText replaces a task's text in place, preserving status and timestamp.
func (s *Store) UpdateText(id, text string) (Task, error) {
	if strings.TrimSpace(text) == "" {
		return Task{}, errors.New("store: empty task text")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.load()
	if err != nil {
		return Task{}, err
	}
	for i, t := range tasks {
		if t.ID == id {
			tasks[i].Text = text
			return tasks[i], s.save(tasks)
		}
	}
	return Task{}, fmt.Errorf("store: unknown id %q", id)
}

func (s *Store) SetStatus(id, status string) (Task, error) {
	if !validStatus(status) {
		return Task{}, errInvalidStatus(status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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

// SetAuthor records which commit author a task came from (team reports).
func (s *Store) SetAuthor(id, author string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.load()
	if err != nil {
		return Task{}, err
	}
	for i, t := range tasks {
		if t.ID == id {
			tasks[i].Author = author
			return tasks[i], s.save(tasks)
		}
	}
	return Task{}, fmt.Errorf("store: unknown id %q", id)
}

// SetBranch records the branch a task's commit was made on.
func (s *Store) SetBranch(id, branch string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.load()
	if err != nil {
		return Task{}, err
	}
	for i, t := range tasks {
		if t.ID == id {
			tasks[i].Branch = branch
			return tasks[i], s.save(tasks)
		}
	}
	return Task{}, fmt.Errorf("store: unknown id %q", id)
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// ApplyBatch validates and applies operations in order, then persists them in
// one write. A validation error leaves the store unchanged.
func (s *Store) ApplyBatch(operations []BatchOperation) ([]Change, error) {
	if len(operations) == 0 {
		return []Change{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.load()
	if err != nil {
		return nil, err
	}
	changes := make([]Change, 0, len(operations))
	for i, operation := range operations {
		var change Change
		tasks, change, err = s.applyOperation(tasks, operation)
		if err != nil {
			return nil, fmt.Errorf("store: batch operation %d: %w", i+1, err)
		}
		changes = append(changes, change)
	}
	if err := s.save(tasks); err != nil {
		return nil, err
	}
	return changes, nil
}

func (s *Store) applyOperation(tasks []Task, operation BatchOperation) ([]Task, Change, error) {
	switch operation.Kind {
	case OperationCreate:
		status := operation.Status
		if status == "" {
			status = "todo"
		}
		if !validStatus(status) {
			return tasks, Change{}, errInvalidStatus(status)
		}
		if strings.TrimSpace(operation.Text) == "" {
			return tasks, Change{}, errors.New("empty task text")
		}
		ts := operation.Timestamp
		if ts.IsZero() {
			ts = s.now()
		}
		task := Task{ID: uuid.NewString(), Text: operation.Text, Status: status, Timestamp: ts}
		return append(tasks, task), Change{Kind: operation.Kind, After: taskPtr(task)}, nil
	case OperationEdit, OperationStatus, OperationDelete:
		index := taskIndex(tasks, operation.ID)
		if index < 0 {
			return tasks, Change{}, fmt.Errorf("unknown id %q", operation.ID)
		}
		before := tasks[index]
		switch operation.Kind {
		case OperationEdit:
			if strings.TrimSpace(operation.Text) == "" {
				return tasks, Change{}, errors.New("empty task text")
			}
			tasks[index].Text = operation.Text
			return tasks, Change{Kind: operation.Kind, Before: taskPtr(before), After: taskPtr(tasks[index])}, nil
		case OperationStatus:
			if !validStatus(operation.Status) {
				return tasks, Change{}, errInvalidStatus(operation.Status)
			}
			tasks[index].Status = operation.Status
			return tasks, Change{Kind: operation.Kind, Before: taskPtr(before), After: taskPtr(tasks[index])}, nil
		case OperationDelete:
			tasks = append(tasks[:index:index], tasks[index+1:]...)
			return tasks, Change{Kind: operation.Kind, Before: taskPtr(before)}, nil
		}
	}
	return tasks, Change{}, fmt.Errorf("unknown operation kind %q", operation.Kind)
}

func taskIndex(tasks []Task, id string) int {
	for i := range tasks {
		if tasks[i].ID == id {
			return i
		}
	}
	return -1
}

func taskPtr(task Task) *Task {
	return &task
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
	return s == "todo" || s == "in-progress" || s == "blocked" || s == "done"
}

func errInvalidStatus(s string) error {
	return fmt.Errorf("store: invalid status %q (valid: todo, in-progress, blocked, done)", s)
}

func dayNum(t time.Time) int {
	y, m, d := t.Date()
	return y*10000 + int(m)*100 + d
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
