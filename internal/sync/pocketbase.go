package sync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"standup/internal/store"
)

// pbTimeout bounds every PocketBase request. Var, not config: nobody asked
// to tune it (same stand as modelTimeout in internal/agent).
var pbTimeout = 30 * time.Second

// PBClient talks to a PocketBase server over its REST API: superuser auth,
// one collection, task records keyed by a unique task_id text field
// (PocketBase record ids are 15 chars, our uuids are 36). Timestamps travel
// as RFC3339Nano text fields — PocketBase date fields normalize to UTC, which
// would corrupt the text|day dedupe key for commits near midnight, and drop
// sub-second precision, which would make every local record read as newer
// than its own remote copy and re-push on every sync.
type PBClient struct {
	base, collection, email, password string
	hc                                *http.Client
	token                             string
}

func NewPB(base, collection, email, password string) *PBClient {
	return &PBClient{
		base:       strings.TrimSuffix(base, "/"),
		collection: collection,
		email:      email,
		password:   password,
		hc:         &http.Client{Timeout: pbTimeout},
	}
}

// pbRecord is one record of the standup collection (all text fields;
// empty strings mean unset).
type pbRecord struct {
	ID      string `json:"id"`
	TaskID  string `json:"task_id"`
	Text    string `json:"text"`
	Status  string `json:"status"`
	Author  string `json:"author"`
	Branch  string `json:"branch"`
	TS      string `json:"ts"`
	Mod     string `json:"mod"`
	Deleted string `json:"deleted"`
}

// authenticate logs in as the superuser and keeps the token in memory.
func (c *PBClient) authenticate() error {
	status, body, err := c.do(http.MethodPost, c.base+"/api/collections/_superusers/auth-with-password",
		map[string]string{"identity": c.email, "password": c.password})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("sync: pocketbase auth failed (check PB_EMAIL / PB_PASSWORD): %s", pbErrorMessage(body, status))
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Token == "" {
		return errors.New("sync: pocketbase auth: no token in response")
	}
	c.token = resp.Token
	return nil
}

// ensureCollection creates the task collection on first sync (404), with a
// unique index on task_id and no API rules (superuser-only access).
func (c *PBClient) ensureCollection() error {
	if !validCollectionName(c.collection) {
		return fmt.Errorf("sync: invalid pocketbase collection name %q", c.collection)
	}
	status, body, err := c.do(http.MethodGet, c.base+"/api/collections/"+c.collection, nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("sync: pocketbase collection check: %s", pbErrorMessage(body, status))
	}
	fields := []map[string]any{
		{"name": "task_id", "type": "text", "required": true},
		{"name": "text", "type": "text", "required": true},
		{"name": "status", "type": "text", "required": true},
		{"name": "author", "type": "text"},
		{"name": "branch", "type": "text"},
		{"name": "ts", "type": "text", "required": true},
		{"name": "mod", "type": "text"},
		{"name": "deleted", "type": "text"},
	}
	payload := map[string]any{
		"name":   c.collection,
		"type":   "base",
		"fields": fields,
		"indexes": []string{
			// Index names are database-global in PocketBase: derive it from
			// the collection so a second standup collection can provision.
			fmt.Sprintf("CREATE UNIQUE INDEX `idx_%s_task_id` ON `%s` (task_id)", c.collection, c.collection),
		},
	}
	status, body, err = c.do(http.MethodPost, c.base+"/api/collections", payload)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("sync: pocketbase create collection: %s", pbErrorMessage(body, status))
	}
	return nil
}

func validCollectionName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// Fetch returns every remote record and the task id → PocketBase record id
// map Push needs to pick PATCH over POST.
func (c *PBClient) Fetch() ([]store.Task, map[string]string, error) {
	var tasks []store.Task
	ids := map[string]string{}
	for page := 1; ; page++ {
		// sort=id: page-based paging over an unordered list can skip or
		// repeat records when the server reorders between pages.
		url := fmt.Sprintf("%s/api/collections/%s/records?perPage=500&skipTotal=true&sort=id&page=%d", c.base, c.collection, page)
		status, body, err := c.do(http.MethodGet, url, nil)
		if err != nil {
			return nil, nil, err
		}
		if status != http.StatusOK {
			return nil, nil, fmt.Errorf("sync: pocketbase list records: %s", pbErrorMessage(body, status))
		}
		var list struct {
			Items []pbRecord `json:"items"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, nil, fmt.Errorf("sync: pocketbase list parse: %w", err)
		}
		for _, r := range list.Items {
			t, err := r.toTask()
			if err != nil {
				return nil, nil, err
			}
			tasks = append(tasks, t)
			ids[r.TaskID] = r.ID
		}
		if len(list.Items) < 500 {
			break
		}
	}
	return tasks, ids, nil
}

// Push writes records to the remote: PATCH when the fetch map knows the
// PocketBase record id, POST otherwise. All fields are always sent — a
// PATCH missing a field keeps its old value, so empties must be explicit.
func (c *PBClient) Push(tasks []store.Task, pbIDs map[string]string) error {
	for _, t := range tasks {
		payload := taskRecord(t)
		method, url := http.MethodPost, c.base+"/api/collections/"+c.collection+"/records"
		if pbID, ok := pbIDs[t.ID]; ok {
			method, url = http.MethodPatch, c.base+"/api/collections/"+c.collection+"/records/"+pbID
		}
		status, body, err := c.do(method, url, payload)
		if err != nil {
			return fmt.Errorf("sync: push task %s: %w", short(t.ID), err)
		}
		if status >= 300 {
			return fmt.Errorf("sync: push task %s: %s", short(t.ID), pbErrorMessage(body, status))
		}
	}
	return nil
}

// toTask maps a remote record, validating what the store would otherwise
// reject anonymously at save time. The fix for a bad record is on the
// server, so every error names the record and says so.
func (t *pbRecord) toTask() (store.Task, error) {
	if strings.TrimSpace(t.TaskID) == "" {
		return store.Task{}, fmt.Errorf("sync: pocketbase record %q: empty task_id — fix or delete it in the pocketbase dashboard", t.ID)
	}
	if strings.TrimSpace(t.Text) == "" {
		return store.Task{}, t.invalid("empty text")
	}
	if !store.ValidStatus(t.Status) {
		return store.Task{}, t.invalid(fmt.Sprintf("invalid status %q (valid: todo, in-progress, blocked, done)", t.Status))
	}
	var out store.Task
	out.ID = t.TaskID
	out.Text = t.Text
	out.Status = t.Status
	out.Author = t.Author
	out.Branch = t.Branch
	ts, err := time.Parse(time.RFC3339, t.TS)
	if err != nil {
		return store.Task{}, t.invalid(fmt.Sprintf("bad ts %q: %v", t.TS, err))
	}
	out.Timestamp = ts
	if t.Mod != "" {
		mod, err := time.Parse(time.RFC3339, t.Mod)
		if err != nil {
			return store.Task{}, t.invalid(fmt.Sprintf("bad mod %q: %v", t.Mod, err))
		}
		out.Updated = mod
	}
	if t.Deleted != "" {
		del, err := time.Parse(time.RFC3339, t.Deleted)
		if err != nil {
			return store.Task{}, t.invalid(fmt.Sprintf("bad deleted %q: %v", t.Deleted, err))
		}
		out.Deleted = del
	}
	return out, nil
}

// invalid describes a rejected remote record: which one, what is wrong, and
// where to fix it.
func (t *pbRecord) invalid(reason string) error {
	return fmt.Errorf("sync: pocketbase record %q (id %s): %s — fix it in the pocketbase dashboard", t.TaskID, t.ID, reason)
}

func taskRecord(t store.Task) map[string]any {
	return map[string]any{
		"task_id": t.ID,
		"text":    t.Text,
		"status":  t.Status,
		"author":  t.Author,
		"branch":  t.Branch,
		"ts":      t.Timestamp.Format(time.RFC3339Nano),
		"mod":     formatOrEmpty(t.Updated),
		"deleted": formatOrEmpty(t.Deleted),
	}
}

func formatOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// do executes one request; the auth token rides along when present. Body
// close errors join the returned error (the cli.go pattern).
func (c *PBClient) do(method, url string, payload any) (status int, body []byte, err error) {
	var rdr io.Reader
	if payload != nil {
		b, e := json.Marshal(payload)
		if e != nil {
			return 0, nil, e
		}
		rdr = bytes.NewReader(b)
	}
	req, e := http.NewRequest(method, url, rdr)
	if e != nil {
		return 0, nil, e
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", c.token)
	}
	resp, e := c.hc.Do(req)
	if e != nil {
		return 0, nil, fmt.Errorf("%w (check network)", e)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()
	data, e := io.ReadAll(resp.Body)
	if e != nil {
		return 0, nil, e
	}
	return resp.StatusCode, data, nil
}

// pbErrorMessage extracts PocketBase's {"message", "data"} error shape,
// falling back to the status text. The per-field data messages carry the
// actual cause ("Failed to create collection." alone says nothing), so they
// are appended in a stable order.
func pbErrorMessage(body []byte, status int) string {
	var e struct {
		Message string                     `json:"message"`
		Data    map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &e); err != nil || e.Message == "" {
		return fmt.Sprintf("%d %s", status, http.StatusText(status))
	}
	details := make([]string, 0, len(e.Data))
	for field, raw := range e.Data {
		if d := fieldDetail(raw); d != "" {
			details = append(details, field+": "+d)
		}
	}
	if len(details) == 0 {
		return e.Message
	}
	sort.Strings(details)
	return e.Message + " (" + strings.Join(details, "; ") + ")"
}

// fieldDetail pulls the message out of one entry of PocketBase's error
// data: either {"message": ...} or a map of those (nested field errors).
func fieldDetail(raw json.RawMessage) string {
	var one struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &one); err == nil && one.Message != "" {
		return one.Message
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nested); err != nil {
		return ""
	}
	parts := make([]string, 0, len(nested))
	for _, sub := range nested {
		if d := fieldDetail(sub); d != "" {
			parts = append(parts, d)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}
