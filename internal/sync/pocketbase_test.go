package sync

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"standup/internal/store"
)

// pbRequest records one call the client made to the fake server.
type pbRequest struct {
	method, path string
	query        url.Values
	auth         string
	body         map[string]any
}

// fakePB is a minimal PocketBase: superuser auth, one collection, records.
type fakePB struct {
	t          *testing.T
	requests   []pbRequest
	records    []map[string]any
	collection string
	exists     bool
	failAuth   bool
	failList   int
	failPatch  int
}

func newFakePB(t *testing.T) (*fakePB, *httptest.Server) {
	t.Helper()
	f := &fakePB{t: t, collection: "standup_tasks"}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakePB) handle(w http.ResponseWriter, r *http.Request) {
	rec := pbRequest{method: r.Method, path: r.URL.Path, query: r.URL.Query(), auth: r.Header.Get("Authorization")}
	if r.Body != nil {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			rec.body = body
		}
	}
	f.requests = append(f.requests, rec)

	if r.URL.Path == "/api/collections/_superusers/auth-with-password" {
		f.auth(w, rec)
		return
	}
	if rec.auth != "test-token" {
		writePBError(w, http.StatusUnauthorized, "The request requires valid record authorization token.")
		return
	}
	for _, route := range []struct {
		match  bool
		handle func(http.ResponseWriter, pbRequest)
	}{
		{r.URL.Path == "/api/collections/"+f.collection && r.Method == http.MethodGet, f.getCollection},
		{r.URL.Path == "/api/collections" && r.Method == http.MethodPost, f.createCollection},
		{r.URL.Path == f.recordsPath() && r.Method == http.MethodGet, f.listRecords},
		{r.URL.Path == f.recordsPath() && r.Method == http.MethodPost, f.createRecord},
		{strings.HasPrefix(r.URL.Path, f.recordsPath()+"/") && r.Method == http.MethodPatch, f.updateRecord},
	} {
		if route.match {
			route.handle(w, rec)
			return
		}
	}
	writePBError(w, http.StatusNotFound, "no such route: "+r.Method+" "+r.URL.Path)
}

func (f *fakePB) recordsPath() string { return "/api/collections/" + f.collection + "/records" }

func (f *fakePB) auth(w http.ResponseWriter, rec pbRequest) {
	if f.failAuth || rec.body["identity"] != "admin@example.com" || rec.body["password"] != "secret" {
		writePBError(w, http.StatusBadRequest, "Failed to authenticate.")
		return
	}
	writePBJSON(w, map[string]any{"token": "test-token"})
}

func (f *fakePB) getCollection(w http.ResponseWriter, _ pbRequest) {
	if !f.exists {
		writePBError(w, http.StatusNotFound, "The requested resource wasn't found.")
		return
	}
	writePBJSON(w, map[string]any{"name": f.collection, "type": "base"})
}

func (f *fakePB) createCollection(w http.ResponseWriter, rec pbRequest) {
	f.exists = true
	writePBJSON(w, map[string]any{"name": rec.body["name"], "type": rec.body["type"]})
}

func (f *fakePB) listRecords(w http.ResponseWriter, rec pbRequest) {
	if f.failList != 0 {
		writePBError(w, f.failList, "list failed")
		return
	}
	f.list(w, rec.query)
}

func (f *fakePB) createRecord(w http.ResponseWriter, rec pbRequest) {
	rec.body["id"] = fmt.Sprintf("pb%013d", len(f.records))
	f.records = append(f.records, rec.body)
	writePBJSON(w, rec.body)
}

func (f *fakePB) updateRecord(w http.ResponseWriter, rec pbRequest) {
	if f.failPatch != 0 {
		writePBError(w, f.failPatch, "update failed")
		return
	}
	id := strings.TrimPrefix(rec.path, f.recordsPath()+"/")
	for i, existing := range f.records {
		if existing["id"] == id {
			rec.body["id"] = id
			f.records[i] = rec.body
			writePBJSON(w, rec.body)
			return
		}
	}
	writePBError(w, http.StatusNotFound, "The requested resource wasn't found.")
}

func (f *fakePB) list(w http.ResponseWriter, q url.Values) {
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(q.Get("perPage"))
	if perPage < 1 {
		perPage = 30
	}
	start := (page - 1) * perPage
	items := []map[string]any{}
	for i := start; i < start+perPage && i < len(f.records); i++ {
		items = append(items, f.records[i])
	}
	writePBJSON(w, map[string]any{"page": page, "perPage": perPage, "totalItems": -1, "totalPages": -1, "items": items})
}

func writePBJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(err)
	}
}

func writePBError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{"status": status, "message": msg, "data": map[string]any{}}); err != nil {
		panic(err)
	}
}

func seedRecord(id, text, ts string) map[string]any {
	return map[string]any{
		"id": "pb" + fmt.Sprintf("%013d", len(id)), "task_id": id, "text": text,
		"status": "todo", "author": "", "branch": "", "ts": ts, "mod": "", "deleted": "",
	}
}

func authedClient(t *testing.T, srv string) *PBClient {
	t.Helper()
	c := NewPB(srv, "standup_tasks", "admin@example.com", "secret")
	require.NoError(t, c.authenticate())
	return c
}

func TestPBAuthSendsCredentials(t *testing.T) {
	f, srv := newFakePB(t)
	_ = authedClient(t, srv.URL)

	require.NotEmpty(t, f.requests)
	auth := f.requests[0]
	assert.Equal(t, http.MethodPost, auth.method)
	assert.Equal(t, "/api/collections/_superusers/auth-with-password", auth.path)
	assert.Equal(t, "admin@example.com", auth.body["identity"])
	assert.Equal(t, "secret", auth.body["password"])
}

func TestPBAuthFailure(t *testing.T) {
	f, srv := newFakePB(t)
	f.failAuth = true
	c := NewPB(srv.URL, "standup_tasks", "admin@example.com", "wrong")
	err := c.authenticate()
	assert.ErrorContains(t, err, "auth")
	assert.ErrorContains(t, err, "PB_EMAIL")
}

func TestPBEnsureCollectionExists(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true
	c := authedClient(t, srv.URL)
	require.NoError(t, c.ensureCollection())
	for _, r := range f.requests {
		assert.NotEqual(t, http.MethodPost+" /api/collections", r.method+" "+r.path, "no create call when the collection exists")
	}
}

func TestPBEnsureCollectionAutoProvisions(t *testing.T) {
	f, srv := newFakePB(t)
	c := authedClient(t, srv.URL)
	require.NoError(t, c.ensureCollection())
	assert.True(t, f.exists, "the fake now has the collection")

	var create *pbRequest
	for i, r := range f.requests {
		if r.method == http.MethodPost && r.path == "/api/collections" {
			create = &f.requests[i]
		}
	}
	require.NotNil(t, create, "a create call was made on 404")
	assert.Equal(t, "standup_tasks", create.body["name"])
	assert.Equal(t, "base", create.body["type"])
	fields, ok := create.body["fields"].([]any)
	require.True(t, ok)
	assert.Len(t, fields, 8, "task_id, text, status, author, branch, ts, mod, deleted")
	indexes, ok := create.body["indexes"].([]any)
	require.True(t, ok)
	require.Len(t, indexes, 1)
	assert.Contains(t, indexes[0], "task_id", "unique index on task_id")
	assert.Contains(t, indexes[0], "UNIQUE")
}

func TestPBEnsureCollectionBadName(t *testing.T) {
	_, srv := newFakePB(t)
	c := NewPB(srv.URL, "standup`; DROP TABLE", "admin@example.com", "secret")
	require.NoError(t, c.authenticate())
	assert.ErrorContains(t, c.ensureCollection(), "collection")
}

func TestPBFetchMapsRecords(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true
	f.records = []map[string]any{
		{
			"id": "pb0000000000001", "task_id": "aa1", "text": "ship it", "status": "done",
			"author": "alice@example.com", "branch": "main",
			"ts": "2026-08-14T16:42:00+02:00", "mod": "2026-08-15T09:00:00Z", "deleted": "2026-08-15T10:00:00Z",
		},
		seedRecord("bb2", "plain", "2026-08-15T08:00:00Z"),
	}
	c := authedClient(t, srv.URL)

	tasks, pbIDs, err := c.Fetch()
	require.NoError(t, err)
	require.Len(t, tasks, 2)

	first := tasks[0]
	assert.Equal(t, "aa1", first.ID)
	assert.Equal(t, "ship it", first.Text)
	assert.Equal(t, "done", first.Status)
	assert.Equal(t, "alice@example.com", first.Author)
	assert.Equal(t, "main", first.Branch)
	ts, err := time.Parse(time.RFC3339, "2026-08-14T16:42:00+02:00")
	require.NoError(t, err)
	assert.True(t, first.Timestamp.Equal(ts), "offset-bearing timestamps round-trip")
	mod, err := time.Parse(time.RFC3339, "2026-08-15T09:00:00Z")
	require.NoError(t, err)
	assert.True(t, first.Updated.Equal(mod))
	del, err := time.Parse(time.RFC3339, "2026-08-15T10:00:00Z")
	require.NoError(t, err)
	assert.True(t, first.Deleted.Equal(del))

	assert.True(t, tasks[1].Updated.IsZero(), "empty mod maps to zero")
	assert.True(t, tasks[1].Deleted.IsZero(), "empty deleted maps to zero")

	assert.Equal(t, "pb0000000000001", pbIDs["aa1"], "fetch yields the task id to record id map")
}

func TestPBFetchPaginates(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true
	for i := 0; i < 501; i++ {
		f.records = append(f.records, seedRecord(fmt.Sprintf("id-%04d", i), "task", "2026-08-15T08:00:00Z"))
	}
	c := authedClient(t, srv.URL)

	tasks, _, err := c.Fetch()
	require.NoError(t, err)
	assert.Len(t, tasks, 501)

	var pages []string
	for _, r := range f.requests {
		if r.method == http.MethodGet && strings.HasSuffix(r.path, "/records") {
			pages = append(pages, r.query.Get("page"))
		}
	}
	assert.Equal(t, []string{"1", "2"}, pages, "follows pages until a short page")
}

func TestPBFetchError(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true
	f.failList = http.StatusForbidden
	c := authedClient(t, srv.URL)
	_, _, err := c.Fetch()
	assert.ErrorContains(t, err, "list failed")
}

// A record hand-edited in the PocketBase dashboard must be rejected with
// the remote coordinates, not an anonymous store error: the fix is on the
// server, so the message has to say which record.
func TestPBFetchRejectsInvalidRemoteStatus(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true
	bad := seedRecord("cc3", "typed in the dashboard", "2026-08-15T08:00:00Z")
	bad["status"] = "in progress"
	f.records = []map[string]any{bad}
	c := authedClient(t, srv.URL)

	_, _, err := c.Fetch()
	require.Error(t, err)
	assert.ErrorContains(t, err, "cc3", "the offending record is named")
	assert.ErrorContains(t, err, "in progress", "so is the bad value")
	assert.ErrorContains(t, err, "pocketbase", "and where to fix it")
}

func TestPBFetchRejectsEmptyRemoteText(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true
	blank := seedRecord("cc3", "  ", "2026-08-15T08:00:00Z")
	f.records = []map[string]any{blank}
	c := authedClient(t, srv.URL)

	_, _, err := c.Fetch()
	require.Error(t, err)
	assert.ErrorContains(t, err, "cc3")
	assert.ErrorContains(t, err, "text")
}

func TestPBFetchRejectsEmptyRemoteTaskID(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true
	anon := seedRecord("", "no task id", "2026-08-15T08:00:00Z")
	f.records = []map[string]any{anon}
	c := authedClient(t, srv.URL)

	_, _, err := c.Fetch()
	assert.ErrorContains(t, err, "task_id")
}

// Page-based pagination without an explicit order can skip or repeat records
// when the server reorders between pages; ask for a stable one.
func TestPBFetchRequestsStableOrder(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true
	f.records = []map[string]any{seedRecord("aa1", "task", "2026-08-15T08:00:00Z")}
	c := authedClient(t, srv.URL)

	_, _, err := c.Fetch()
	require.NoError(t, err)
	var sorted []string
	for _, r := range f.requests {
		if r.method == http.MethodGet && strings.HasSuffix(r.path, "/records") {
			sorted = append(sorted, r.query.Get("sort"))
		}
	}
	require.NotEmpty(t, sorted)
	assert.Equal(t, "id", sorted[0], "list requests carry a deterministic sort")
}

func TestPBPushPostsNewAndPatchesKnown(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true
	f.records = []map[string]any{seedRecord("aa1", "old text", "2026-08-15T08:00:00Z")}
	c := authedClient(t, srv.URL)

	ts := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	known := store.Task{ID: "aa1", Text: "new text", Status: "done", Timestamp: ts, Updated: ts.Add(time.Hour)}
	fresh := store.Task{ID: "bb2", Text: "brand new", Status: "todo", Timestamp: ts, Updated: ts}
	gone := store.Task{ID: "cc3", Text: "deleted", Status: "todo", Timestamp: ts, Updated: ts, Deleted: ts.Add(2 * time.Hour)}
	pbIDs := map[string]string{"aa1": "pb0000000000003"} // seedRecord derives the id from len("aa1")

	require.NoError(t, c.Push([]store.Task{known, fresh, gone}, pbIDs))

	var methods []string
	var bodies []map[string]any
	for _, r := range f.requests {
		if r.method == http.MethodPost && strings.HasSuffix(r.path, "/records") || r.method == http.MethodPatch {
			methods = append(methods, r.method+" "+r.path)
			bodies = append(bodies, r.body)
		}
	}
	assert.Equal(t, []string{
		"PATCH /api/collections/standup_tasks/records/pb0000000000003",
		"POST /api/collections/standup_tasks/records",
		"POST /api/collections/standup_tasks/records",
	}, methods)

	assert.Equal(t, "new text", bodies[0]["text"])
	assert.Equal(t, "done", bodies[0]["status"])
	assert.Equal(t, ts.Add(time.Hour).Format(time.RFC3339), bodies[0]["mod"])
	assert.Equal(t, "brand new", bodies[1]["text"])
	assert.Equal(t, ts.Add(2*time.Hour).Format(time.RFC3339), bodies[2]["deleted"], "tombstones push their deletion time")
}

// A record must survive the wire unchanged, sub-second precision included:
// a truncated timestamp reads as a local edit and re-pushes on every sync.
func TestPBRecordRoundTripKeepsPrecision(t *testing.T) {
	ts := time.Date(2026, 8, 19, 21, 56, 1, 737673799, time.UTC)
	task := store.Task{ID: "aa1", Text: "only once", Status: "todo", Timestamp: ts, Updated: ts, Deleted: ts.Add(time.Second / 3)}
	rec := taskRecord(task)

	back := pbRecord{
		TaskID: task.ID, Text: task.Text, Status: task.Status,
		TS: rec["ts"].(string), Mod: rec["mod"].(string), Deleted: rec["deleted"].(string),
	}
	got, err := back.toTask()
	require.NoError(t, err)
	assert.True(t, sameRecord(task, got), "the record round-trips unchanged")
}

func TestPBPushErrorNamesTask(t *testing.T) {
	f, srv := newFakePB(t)
	f.exists = true
	f.failPatch = http.StatusBadRequest
	c := authedClient(t, srv.URL)

	ts := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	bad := store.Task{ID: "aa123456-7890", Text: "x", Status: "todo", Timestamp: ts, Updated: ts}
	err := c.Push([]store.Task{bad}, map[string]string{"aa123456-7890": "pb0000000000000"})
	assert.ErrorContains(t, err, "aa123456")
}
