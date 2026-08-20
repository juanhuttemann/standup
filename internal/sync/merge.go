// Package sync merges the local task store with a remote backend
// (PocketBase) deterministically: union by id, last-writer-wins per record,
// tombstones for deletes. No model, no clock — record times are data.
package sync

import (
	"sort"
	"time"

	"standup/internal/store"
)

// Result is the outcome of a Merge: the full merged set (tombstones
// included), the records the remote still needs (Push), and how many
// records were taken from the remote (Pulled) or tombstoned as parallel
// imports (Resolved).
type Result struct {
	Merged   []store.Task
	Push     []store.Task
	Pulled   int
	Resolved int
}

// Merge unions local and remote by task id. When both sides carry a record,
// the one with the newer event time — max(ModTime, Deleted) — wins; ties go
// to the remote (a documented, deterministic tie-break). Live duplicates
// from parallel commit imports (same text, same day — the importCommits key)
// are resolved by tombstoning all but the earliest (tie: lowest id), and the
// fresh tombstones join Push so every machine converges.
func Merge(local, remote []store.Task) Result {
	byID := make(map[string]store.Task, len(local)+len(remote))
	push := make(map[string]store.Task, len(local))
	var res Result

	for _, l := range local {
		byID[l.ID] = l
		push[l.ID] = l
	}
	for _, r := range remote {
		l, ok := byID[r.ID]
		if !ok {
			byID[r.ID] = r
			res.Pulled++
			continue
		}
		if sameRecord(l, r) {
			delete(push, r.ID)
			continue
		}
		le, re := eventTime(l), eventTime(r)
		if re.After(le) || re.Equal(le) {
			byID[r.ID] = r
			delete(push, r.ID)
			res.Pulled++
		}
	}

	res.Merged = make([]store.Task, 0, len(byID))
	for _, t := range byID {
		res.Merged = append(res.Merged, t)
	}
	res.Resolved = dedupe(res.Merged, push)

	sort.SliceStable(res.Merged, func(i, j int) bool {
		a, b := res.Merged[i], res.Merged[j]
		if a.Timestamp.Equal(b.Timestamp) {
			return a.ID < b.ID
		}
		return a.Timestamp.Before(b.Timestamp)
	})
	res.Push = make([]store.Task, 0, len(push))
	for _, t := range push {
		res.Push = append(res.Push, t)
	}
	sort.Slice(res.Push, func(i, j int) bool { return res.Push[i].ID < res.Push[j].ID })
	return res
}

// eventTime is a record's last event: the modification, or the deletion
// when that came later.
func eventTime(t store.Task) time.Time {
	mt := t.ModTime()
	if t.Deleted.After(mt) {
		return t.Deleted
	}
	return mt
}

// sameRecord reports whether two records carry the same content (times
// compared by instant, so a JSON round-trip never reads as a change).
func sameRecord(a, b store.Task) bool {
	return a.Text == b.Text && a.Status == b.Status && a.Author == b.Author && a.Branch == b.Branch &&
		a.Timestamp.Equal(b.Timestamp) && a.ModTime().Equal(b.ModTime()) && a.Deleted.Equal(b.Deleted)
}

// dedupe tombstones live duplicates (same text, same day — the key
// importCommits dedupes on, so parallel imports on two machines converge).
// The earliest event time survives (tie: lowest id); losers are tombstoned
// at their own event time and added to push. Returns the loser count.
func dedupe(merged []store.Task, push map[string]store.Task) int {
	groups := map[string][]int{}
	for i, t := range merged {
		if !t.Deleted.IsZero() {
			continue
		}
		k := t.Text + "|" + t.Timestamp.Format("2006-01-02")
		groups[k] = append(groups[k], i)
	}
	resolved := 0
	for _, idxs := range groups {
		if len(idxs) < 2 {
			continue
		}
		sort.Slice(idxs, func(i, j int) bool {
			a, b := merged[idxs[i]], merged[idxs[j]]
			ae, be := eventTime(a), eventTime(b)
			if ae.Equal(be) {
				return a.ID < b.ID
			}
			return ae.Before(be)
		})
		for _, loser := range idxs[1:] {
			l := merged[loser]
			l.Deleted = eventTime(l)
			merged[loser] = l
			push[l.ID] = l
			resolved++
		}
	}
	return resolved
}
