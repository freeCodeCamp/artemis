package gc

import (
	"fmt"
	"sort"
)

type Plan struct {
	Site       string
	Delete     []Deploy
	TotalBytes int64
	Aborted    bool
	Reason     string
}

func sortNewestFirst(ds []Deploy) {
	sort.SliceStable(ds, func(i, j int) bool {
		if !ds[i].Mtime.Equal(ds[j].Mtime) {
			return ds[i].Mtime.After(ds[j].Mtime)
		}
		return ds[i].ID > ds[j].ID
	})
}

func PlanSite(site string, in RetainInput, p Policy, blastCap int) Plan {
	_, del := Retain(in, p)
	del = append(del, in.Expired...)
	sortNewestFirst(del)

	plan := Plan{Site: site}
	if len(del) > 0 && blastCap <= 0 {
		plan.Aborted = true
		plan.Reason = fmt.Sprintf(
			"refusing %d deletes: blast-cap 0 means no ceiling was configured", len(del))
		return plan
	}
	if blastCap > 0 && len(del) > blastCap {
		plan.Aborted = true
		plan.Reason = fmt.Sprintf("delete plan of %d exceeds blast-cap %d; reaping oldest %d this run", len(del), blastCap, blastCap)
		del = del[len(del)-blastCap:]
	}
	plan.Delete = del
	for _, d := range del {
		plan.TotalBytes += d.Bytes
	}
	return plan
}
