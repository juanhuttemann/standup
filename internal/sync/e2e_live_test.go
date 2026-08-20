//go:build e2e

package sync

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"standup/internal/store"
)

// Live PocketBase e2e, driven by the same variables the CLI uses:
//
//	PB_URL=http://127.0.0.1:8090 PB_EMAIL=… PB_PASSWORD=… \
//	  go test -tags e2e ./internal/sync/
//
// No url, no run. Each test provisions its own throwaway collection, so a
// run never touches the collection a real sync uses.
func live(t *testing.T) Server {
	t.Helper()
	url := os.Getenv("PB_URL")
	if url == "" {
		t.Skip("PB_URL not set")
	}
	return Server{
		URL:        url,
		Collection: fmt.Sprintf("e2e_%s_%d", sanitize(t.Name()), time.Now().UnixNano()%100000),
		Email:      os.Getenv("PB_EMAIL"),
		Password:   os.Getenv("PB_PASSWORD"),
	}
}

func sanitize(s string) string {
	out := []rune{}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

func newStore(t *testing.T, name string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), name+".jsonl"))
	require.NoError(t, err)
	return st
}

func rawGET(t *testing.T, srv Server, path string) map[string]any {
	t.Helper()
	c := NewPB(srv.URL, srv.Collection, srv.Email, srv.Password)
	require.NoError(t, c.authenticate())
	status, body, err := c.do(http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, string(body))
	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// 1. First sync on a virgin server: collection auto-provisioned, all pushed.
func TestE2EAutoProvisionAndPush(t *testing.T) {
	srv := live(t)
	st := newStore(t, "a")
	_, err := st.Add("write the sync layer")
	require.NoError(t, err)
	_, err = st.AddWithStatus("review the PR", "in-progress")
	require.NoError(t, err)

	res, err := Run(st, srv)
	require.NoError(t, err)
	assert.Len(t, res.Push, 2)
	assert.Zero(t, res.Pulled)

	// the real collection exists with our schema
	got := rawGET(t, srv, "/api/collections/"+srv.Collection)
	assert.Equal(t, "base", got["type"])
	names := map[string]bool{}
	for _, f := range got["fields"].([]any) {
		names[f.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"task_id", "text", "status", "author", "branch", "ts", "mod", "deleted"} {
		assert.True(t, names[want], "field %s exists on the server", want)
	}
	idx, _ := json.Marshal(got["indexes"])
	assert.Contains(t, string(idx), "task_id")

	recs := rawGET(t, srv, "/api/collections/"+srv.Collection+"/records")
	assert.Len(t, recs["items"], 2)
}

// 2. Two machines converge: B pulls what A pushed.
func TestE2ETwoMachinesConverge(t *testing.T) {
	srv := live(t)
	a, b := newStore(t, "a"), newStore(t, "b")
	_, err := a.Add("task from machine A")
	require.NoError(t, err)
	_, err = b.Add("task from machine B")
	require.NoError(t, err)

	_, err = Run(a, srv)
	require.NoError(t, err)
	resB, err := Run(b, srv)
	require.NoError(t, err)
	assert.Equal(t, 1, resB.Pulled, "B pulled A's task")

	resA2, err := Run(a, srv)
	require.NoError(t, err)
	assert.Equal(t, 1, resA2.Pulled, "A pulled B's task")

	for _, st := range []*store.Store{a, b} {
		l, err := st.List()
		require.NoError(t, err)
		require.Len(t, l, 2)
	}
}

// 3. Idempotency: syncing twice with no changes pushes nothing.
func TestE2EIdempotent(t *testing.T) {
	srv := live(t)
	st := newStore(t, "a")
	_, err := st.Add("only once")
	require.NoError(t, err)

	_, err = Run(st, srv)
	require.NoError(t, err)
	res, err := Run(st, srv)
	require.NoError(t, err)
	assert.Empty(t, res.Push, "nothing to push on a no-op sync")
	assert.Zero(t, res.Pulled)

	recs := rawGET(t, srv, "/api/collections/"+srv.Collection+"/records")
	assert.Len(t, recs["items"], 1, "no duplicate record was created")
}

// 4. Last-writer-wins on a real round trip.
func TestE2EConflictLastWriterWins(t *testing.T) {
	srv := live(t)
	a, b := newStore(t, "a"), newStore(t, "b")
	task, err := a.Add("original wording")
	require.NoError(t, err)
	_, err = Run(a, srv)
	require.NoError(t, err)
	_, err = Run(b, srv)
	require.NoError(t, err)

	time.Sleep(1100 * time.Millisecond) // RFC3339 second resolution
	_, err = b.UpdateText(task.ID, "edited on B")
	require.NoError(t, err)
	_, err = Run(b, srv)
	require.NoError(t, err)

	res, err := Run(a, srv)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Pulled)
	l, err := a.List()
	require.NoError(t, err)
	require.Len(t, l, 1)
	assert.Equal(t, "edited on B", l[0].Text)
}

// 5. Deletes propagate as tombstones.
func TestE2EDeletePropagates(t *testing.T) {
	srv := live(t)
	a, b := newStore(t, "a"), newStore(t, "b")
	task, err := a.Add("doomed task")
	require.NoError(t, err)
	_, err = Run(a, srv)
	require.NoError(t, err)
	_, err = Run(b, srv)
	require.NoError(t, err)

	time.Sleep(1100 * time.Millisecond)
	require.NoError(t, b.Delete(task.ID))
	_, err = Run(b, srv)
	require.NoError(t, err)

	_, err = Run(a, srv)
	require.NoError(t, err)
	l, err := a.List()
	require.NoError(t, err)
	assert.Empty(t, l, "the delete reached machine A")
	snap, err := a.Snapshot()
	require.NoError(t, err)
	require.Len(t, snap, 1)
	assert.False(t, snap[0].Deleted.IsZero(), "kept as a tombstone")
}

// 6. Parallel commit imports (same text, same day, different ids) converge.
func TestE2EParallelImportDedupe(t *testing.T) {
	srv := live(t)
	a, b := newStore(t, "a"), newStore(t, "b")
	when := time.Now().Add(-2 * time.Hour)
	_, err := a.AddAt("fix: login redirect loop", "done", when)
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond)
	_, err = b.AddAt("fix: login redirect loop", "done", when)
	require.NoError(t, err)

	_, err = Run(a, srv)
	require.NoError(t, err)
	resB, err := Run(b, srv)
	require.NoError(t, err)
	assert.Equal(t, 1, resB.Resolved, "B tombstoned its duplicate")

	_, err = Run(a, srv)
	require.NoError(t, err)
	for _, st := range []*store.Store{a, b} {
		l, err := st.List()
		require.NoError(t, err)
		assert.Len(t, l, 1, "one live copy on every machine")
	}
}

// 7. Timestamps keep their offset (the reason ts is a text field).
func TestE2ETimezoneFidelity(t *testing.T) {
	srv := live(t)
	loc := time.FixedZone("UTC+13", 13*3600)
	when := time.Date(2026, 8, 19, 1, 30, 0, 0, loc) // 2026-08-18 12:30 UTC
	a, b := newStore(t, "a"), newStore(t, "b")
	_, err := a.AddAt("late night commit", "done", when)
	require.NoError(t, err)
	_, err = Run(a, srv)
	require.NoError(t, err)
	_, err = Run(b, srv)
	require.NoError(t, err)

	l, err := b.List()
	require.NoError(t, err)
	require.Len(t, l, 1)
	assert.True(t, l[0].Timestamp.Equal(when), "instant preserved")
	_, off := l[0].Timestamp.Zone()
	assert.Equal(t, 13*3600, off, "offset preserved: the local calendar day survives")
	assert.Equal(t, "2026-08-19", l[0].Timestamp.Format("2006-01-02"))
}

// 8. Pagination over the 500-per-page boundary.
func TestE2EPagination(t *testing.T) {
	srv := live(t)
	a, b := newStore(t, "a"), newStore(t, "b")
	base := time.Now().Add(-24 * time.Hour)
	for i := 0; i < 501; i++ {
		_, err := a.AddAt(fmt.Sprintf("bulk task %04d", i), "done", base.Add(time.Duration(i)*time.Second))
		require.NoError(t, err)
	}
	_, err := Run(a, srv)
	require.NoError(t, err)

	res, err := Run(b, srv)
	require.NoError(t, err)
	assert.Equal(t, 501, res.Pulled, "every page was fetched")
}

// 9. The unique index really rejects a duplicate task_id.
func TestE2EUniqueIndexEnforced(t *testing.T) {
	srv := live(t)
	st := newStore(t, "a")
	task, err := st.Add("unique me")
	require.NoError(t, err)
	_, err = Run(st, srv)
	require.NoError(t, err)

	c := NewPB(srv.URL, srv.Collection, srv.Email, srv.Password)
	require.NoError(t, c.authenticate())
	// POST the same task with no known pb id: the server must refuse.
	err = c.Push([]store.Task{task}, map[string]string{})
	assert.Error(t, err, "duplicate task_id rejected by the unique index")
}

// 10. Bad credentials fail with a helpful message.
func TestE2EBadCredentials(t *testing.T) {
	srv := live(t)
	srv.Password = "wrong-password"
	st := newStore(t, "a")
	_, err := st.Add("x")
	require.NoError(t, err)
	_, err = Run(st, srv)
	require.ErrorContains(t, err, "PB_EMAIL")
}
