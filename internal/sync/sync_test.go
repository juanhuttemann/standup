package sync

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"standup/internal/store"
)

// testServer points at the fake with the credentials it accepts.
func testServer(url string) Server {
	return Server{URL: url, Collection: "standup_tasks", Email: "admin@example.com", Password: "secret"}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	return st
}

func TestRunMissingCredentials(t *testing.T) {
	st := openStore(t)
	_, err := Run(st, Server{URL: "http://unused", Collection: "standup_tasks"})
	assert.ErrorContains(t, err, "PB_EMAIL")
	assert.ErrorContains(t, err, "PB_PASSWORD")
}

// Credentials arrive as data, so sync never reads the environment itself
// (internal/config owns env resolution).
func TestRunTakesCredentialsFromCaller(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true
	st := openStore(t)
	st.Now = func() time.Time { return t1 }
	_, err := st.Add("passed in, not read from env")
	require.NoError(t, err)

	res, err := Run(st, testServer(srv.URL))
	require.NoError(t, err)
	assert.Len(t, res.Push, 1)
	require.Len(t, f.records, 1)
	assert.Equal(t, "passed in, not read from env", f.records[0]["text"])
}

func TestRunRoundTrip(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true

	st := openStore(t)
	st.Now = func() time.Time { return t2 }
	local, err := st.Add("edited locally")
	require.NoError(t, err)

	// The remote carries an older copy of the same task plus a new one.
	f.records = []map[string]any{
		{
			"id": "pb0000000000001", "task_id": local.ID, "text": "stale text", "status": "todo",
			"author": "", "branch": "", "ts": t2.Format(time.RFC3339), "mod": t1.Format(time.RFC3339), "deleted": "",
		},
		seedRecord("bb2", "from the other machine", t1.Format(time.RFC3339)),
	}

	res, err := Run(st, testServer(srv.URL))
	require.NoError(t, err)
	assert.Equal(t, 1, res.Pulled, "the remote-only task was pulled")
	assert.Len(t, res.Push, 1, "the newer local task was pushed")

	all, err := st.Snapshot()
	require.NoError(t, err)
	require.Len(t, all, 2)
	texts := map[string]string{}
	for _, tk := range all {
		texts[tk.ID] = tk.Text
	}
	assert.Equal(t, "edited locally", texts[local.ID], "local winner kept")
	assert.Equal(t, "from the other machine", texts["bb2"], "remote task saved locally")

	// The server now holds the pushed local version.
	var patched map[string]any
	for _, r := range f.records {
		if r["task_id"] == local.ID {
			patched = r
		}
	}
	require.NotNil(t, patched)
	assert.Equal(t, "edited locally", patched["text"])
}

func TestRunAutoProvisionsAndPushesAll(t *testing.T) {
	f, srv := newFakePB(t)

	st := openStore(t)
	st.Now = func() time.Time { return t1 }
	_, err := st.Add("first sync")
	require.NoError(t, err)

	res, err := Run(st, testServer(srv.URL))
	require.NoError(t, err)
	assert.True(t, f.exists, "the collection was auto-provisioned")
	assert.Len(t, res.Push, 1, "everything pushes on first sync")
	assert.Len(t, f.records, 1)
	assert.Equal(t, "first sync", f.records[0]["text"])
}

func TestRunFetchFailureLeavesStoreUntouched(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true
	f.failList = http.StatusInternalServerError

	st := openStore(t)
	st.Now = func() time.Time { return t1 }
	_, err := st.Add("precious")
	require.NoError(t, err)

	_, err = Run(st, testServer(srv.URL))
	assert.Error(t, err)

	all, err := st.Snapshot()
	require.NoError(t, err)
	require.Len(t, all, 1, "the local store is untouched after a fetch failure")
	assert.Equal(t, "precious", all[0].Text)
}

func TestRunDeletePropagates(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true

	st := openStore(t)
	st.Now = func() time.Time { return t1 }
	doomed, err := st.Add("deleted here, alive there")
	require.NoError(t, err)
	st.Now = func() time.Time { return t2 }
	require.NoError(t, st.Delete(doomed.ID))

	// The remote still carries the live record.
	f.records = []map[string]any{
		{
			"id": "pb0000000000001", "task_id": doomed.ID, "text": "deleted here, alive there", "status": "todo",
			"author": "", "branch": "", "ts": t1.Format(time.RFC3339), "mod": t1.Format(time.RFC3339), "deleted": "",
		},
	}

	res, err := Run(st, testServer(srv.URL))
	require.NoError(t, err)
	assert.Zero(t, res.Pulled, "the newer local tombstone wins")

	var pushed map[string]any
	for _, r := range f.records {
		if r["task_id"] == doomed.ID {
			pushed = r
		}
	}
	require.NotNil(t, pushed)
	assert.Equal(t, t2.Format(time.RFC3339), pushed["deleted"], "the remote learned the deletion")
}
