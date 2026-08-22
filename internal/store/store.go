package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
)

type Task struct {
	ID        string    `json:"id"`
	Text      string    `json:"task"`
	Status    string    `json:"status"`
	Author    string    `json:"author,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Updated   time.Time `json:"updated,omitzero"` // last modification; zero = pre-sync record
	Deleted   time.Time `json:"deleted,omitzero"` // tombstone; zero = live
}

// ModTime is the last modification time; records from before the sync
// schema (no Updated) fall back to their event timestamp.
func (t Task) ModTime() time.Time {
	if t.Updated.IsZero() {
		return t.Timestamp
	}
	return t.Updated
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

// lockTimeout bounds the wait for another process's store lock: past it the
// holder is stuck and a named error beats an indefinite hang.
var lockTimeout = 10 * time.Second

// lockRetry is the poll interval while waiting for the lock.
const lockRetry = 20 * time.Millisecond

// withLock runs fn as the store file's only writer. Every mutation is a
// read-modify-write of the whole JSONL, so overlapping invocations (an agent
// firing `commits` and `add`, a CI job, a shell loop) would otherwise clobber
// each other and report success. The mutex covers goroutines; the file lock
// covers processes. Reads need neither: save publishes by atomic rename.
func (s *Store) withLock(fn func() error) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	// A lock file beside the store, never the store itself: save replaces the
	// store by rename, so a lock held on it would guard a discarded inode.
	lock := flock.New(s.Path + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), lockTimeout)
	defer cancel()
	locked, lockErr := lock.TryLockContext(ctx, lockRetry)
	switch {
	case errors.Is(lockErr, context.DeadlineExceeded), lockErr == nil && !locked:
		return fmt.Errorf("store: timed out after %s waiting for another standup process to write %s", lockTimeout, s.Path)
	case lockErr != nil:
		return fmt.Errorf("store: lock %s: %w", lock.Path(), lockErr)
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("store: unlock %s: %w", lock.Path(), unlockErr))
		}
	}()
	return fn()
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
	if !ValidStatus(t.Status) {
		return errInValidStatus(t.Status)
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
	if !ValidStatus(status) {
		return Task{}, errInValidStatus(status)
	}
	if strings.TrimSpace(text) == "" {
		return Task{}, errors.New("store: empty task text")
	}
	var t Task
	err := s.withLock(func() error {
		t = Task{ID: uuid.NewString(), Text: text, Status: status, Timestamp: ts, Updated: s.now()}
		tasks, err := s.load()
		if err != nil {
			return err
		}
		return s.save(append(tasks, t))
	})
	if err != nil {
		return Task{}, err
	}
	return t, nil
}

// List returns live tasks (tombstones hidden), ordered by timestamp.
func (s *Store) List() ([]Task, error) {
	tasks, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if t.Deleted.IsZero() {
			out = append(out, t)
		}
	}
	return out, nil
}

// Snapshot returns every record including tombstones: the sync input and
// the commit-import dedupe set (deleted imports must not resurrect).
func (s *Store) Snapshot() ([]Task, error) {
	tasks, err := s.load()
	if err != nil {
		return nil, err
	}
	if tasks == nil {
		tasks = []Task{}
	}
	return tasks, nil
}

// ReplaceAll validates and persists a full task set (a sync merge result);
// a single invalid record rejects the whole replace, leaving the file
// untouched.
func (s *Store) ReplaceAll(tasks []Task) error {
	for _, t := range tasks {
		if err := validateTask(t); err != nil {
			return fmt.Errorf("store: replace: %w", err)
		}
	}
	return s.withLock(func() error { return s.save(tasks) })
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
	return s.mutate(id, func(t *Task) { t.Text = text })
}

func (s *Store) SetStatus(id, status string) (Task, error) {
	if !ValidStatus(status) {
		return Task{}, errInValidStatus(status)
	}
	return s.mutate(id, func(t *Task) { t.Status = status })
}

// SetAuthor records which commit author a task came from (team reports).
func (s *Store) SetAuthor(id, author string) (Task, error) {
	return s.mutate(id, func(t *Task) { t.Author = author })
}

// SetBranch records the branch a task's commit was made on.
func (s *Store) SetBranch(id, branch string) (Task, error) {
	return s.mutate(id, func(t *Task) { t.Branch = branch })
}

func (s *Store) Delete(id string) error {
	_, err := s.mutate(id, func(t *Task) { t.Deleted = s.now() })
	return err
}

// mutate applies apply to the live task with id and persists the result under
// the store lock; every single-field setter shares this read-modify-write.
func (s *Store) mutate(id string, apply func(*Task)) (Task, error) {
	var out Task
	err := s.withLock(func() error {
		tasks, err := s.load()
		if err != nil {
			return err
		}
		index := taskIndex(tasks, id)
		if index < 0 {
			return fmt.Errorf("store: unknown id %q", id)
		}
		apply(&tasks[index])
		tasks[index].Updated = s.now()
		out = tasks[index]
		return s.save(tasks)
	})
	if err != nil {
		return Task{}, err
	}
	return out, nil
}

// ApplyBatch validates and applies operations in order, then persists them in
// one write. A validation error leaves the store unchanged.
func (s *Store) ApplyBatch(operations []BatchOperation) ([]Change, error) {
	if len(operations) == 0 {
		return []Change{}, nil
	}
	var changes []Change
	err := s.withLock(func() error {
		tasks, err := s.load()
		if err != nil {
			return err
		}
		changes = make([]Change, 0, len(operations))
		for i, operation := range operations {
			var change Change
			tasks, change, err = s.applyOperation(tasks, operation)
			if err != nil {
				return fmt.Errorf("store: batch operation %d: %w", i+1, err)
			}
			changes = append(changes, change)
		}
		return s.save(tasks)
	})
	if err != nil {
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
		if !ValidStatus(status) {
			return tasks, Change{}, errInValidStatus(status)
		}
		if strings.TrimSpace(operation.Text) == "" {
			return tasks, Change{}, errors.New("empty task text")
		}
		ts := operation.Timestamp
		if ts.IsZero() {
			ts = s.now()
		}
		task := Task{ID: uuid.NewString(), Text: operation.Text, Status: status, Timestamp: ts, Updated: s.now()}
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
			tasks[index].Updated = s.now()
			return tasks, Change{Kind: operation.Kind, Before: taskPtr(before), After: taskPtr(tasks[index])}, nil
		case OperationStatus:
			if !ValidStatus(operation.Status) {
				return tasks, Change{}, errInValidStatus(operation.Status)
			}
			tasks[index].Status = operation.Status
			tasks[index].Updated = s.now()
			return tasks, Change{Kind: operation.Kind, Before: taskPtr(before), After: taskPtr(tasks[index])}, nil
		case OperationDelete:
			// Tombstone, never drop: a prompt-driven delete has to reach
			// the other machines too. Change still reports no After, so
			// callers keep seeing a deletion.
			tasks[index].Deleted = s.now()
			return tasks, Change{Kind: operation.Kind, Before: taskPtr(before)}, nil
		}
	}
	return tasks, Change{}, fmt.Errorf("unknown operation kind %q", operation.Kind)
}

// taskIndex finds a live task; tombstones are invisible here exactly as
// they are to List, so a deleted id reads as unknown.
func taskIndex(tasks []Task, id string) int {
	for i := range tasks {
		if tasks[i].ID == id && tasks[i].Deleted.IsZero() {
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
		return Task{}, &AmbiguousIDError{Prefix: p, Matches: matches}
	}
}

// AmbiguousIDError carries the tasks a prefix matched. The rows are what the
// user needs to retry, but rendering them is the CLI's job, not the store's.
type AmbiguousIDError struct {
	Prefix  string
	Matches []Task
}

func (e *AmbiguousIDError) Error() string {
	return fmt.Sprintf("store: ambiguous id %q (%d matches)", e.Prefix, len(e.Matches))
}

// ValidStatus reports whether s is one of the four task statuses. Exported
// so sync can reject a hand-edited remote record naming the remote, rather
// than letting an anonymous store error surface at save time.
func ValidStatus(s string) bool {
	return s == "todo" || s == "in-progress" || s == "blocked" || s == "done"
}

func errInValidStatus(s string) error {
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
