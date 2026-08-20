package sync

import (
	"fmt"
	"strings"

	"standup/internal/store"
)

// Server is one PocketBase deployment: url and collection are settings,
// email and password are the superuser credentials. All four arrive as
// data — resolving them is internal/config's job, so the sync layer stays
// testable and has exactly one way in.
type Server struct {
	URL, Collection, Email, Password string
}

// Run executes one sync round: authenticate, ensure the collection exists,
// fetch the remote, merge with the local snapshot, persist the merge
// locally, then push the records the remote is missing. The local store is
// saved before pushing, so a failed push leaves a consistent local state
// and the next sync retries (self-healing).
func Run(st *store.Store, s Server) (Result, error) {
	var missing []string
	if s.Email == "" {
		missing = append(missing, "PB_EMAIL")
	}
	if s.Password == "" {
		missing = append(missing, "PB_PASSWORD")
	}
	if len(missing) > 0 {
		return Result{}, fmt.Errorf("missing required environment variables: %s (needed by sync)", strings.Join(missing, ", "))
	}
	c := NewPB(s.URL, s.Collection, s.Email, s.Password)
	if err := c.authenticate(); err != nil {
		return Result{}, err
	}
	if err := c.ensureCollection(); err != nil {
		return Result{}, err
	}
	remote, pbIDs, err := c.Fetch()
	if err != nil {
		return Result{}, err
	}
	local, err := st.Snapshot()
	if err != nil {
		return Result{}, err
	}
	res := Merge(local, remote)
	if err := st.ReplaceAll(res.Merged); err != nil {
		return Result{}, fmt.Errorf("sync: %w", err)
	}
	if err := c.Push(res.Push, pbIDs); err != nil {
		return res, err
	}
	return res, nil
}
