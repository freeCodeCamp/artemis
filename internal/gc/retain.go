package gc

import (
	"sort"
	"time"
)

type Deploy struct {
	ID        string
	Mtime     time.Time
	Bytes     int64
	HasMarker bool
}

type Policy struct {
	RecentKeep    int
	Grace         time.Duration
	Retention     time.Duration
	ServeCacheTTL time.Duration
}

type RetainInput struct {
	Deploys         []Deploy
	AliasTargets    map[string]struct{}
	LastAliasChange time.Time
	Now             time.Time
}

func Retain(in RetainInput, p Policy) (keep, del []Deploy) {
	ordered := make([]Deploy, len(in.Deploys))
	copy(ordered, in.Deploys)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].Mtime.Equal(ordered[j].Mtime) {
			return ordered[i].Mtime.After(ordered[j].Mtime)
		}
		return ordered[i].ID > ordered[j].ID
	})

	freshAliasMove := !in.LastAliasChange.IsZero() &&
		in.Now.Sub(in.LastAliasChange) < p.ServeCacheTTL

	for rank, d := range ordered {
		if retainDeploy(d, rank, freshAliasMove, in.AliasTargets, in.Now, p) {
			keep = append(keep, d)
		} else {
			del = append(del, d)
		}
	}
	return keep, del
}

func retainDeploy(d Deploy, rank int, freshAliasMove bool, aliases map[string]struct{}, now time.Time, p Policy) bool {
	if _, aliased := aliases[d.ID]; aliased {
		return true
	}
	if rank < p.RecentKeep {
		return true
	}
	if freshAliasMove {
		return true
	}
	age := now.Sub(d.Mtime)
	if age < p.Grace {
		return true
	}
	if d.HasMarker && age < p.Retention {
		return true
	}
	return false
}
