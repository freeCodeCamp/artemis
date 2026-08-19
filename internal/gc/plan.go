package gc

import (
	"fmt"
	"sort"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type Plan struct {
	Site       sitekey.Dirname
	Delete     []Deploy
	TotalBytes int64
	Aborted    bool
	Reason     string
}

func capOldest[T any](items []T, n int, older func(a, b T) bool) []T {
	if n <= 0 {
		return nil
	}
	if len(items) <= n {
		return items
	}
	out := make([]T, len(items))
	copy(out, items)
	sort.SliceStable(out, func(i, j int) bool { return older(out[i], out[j]) })
	return out[:n]
}

func olderDeploy(a, b Deploy) bool {
	if !a.Mtime.Equal(b.Mtime) {
		return a.Mtime.Before(b.Mtime)
	}
	return a.ID < b.ID
}

func olderID(a, b string) bool { return a < b }

func PlanSite(site sitekey.Dirname, in RetainInput, p Policy, blastCap int) Plan {
	_, del := Retain(in, p)
	del = append(del, in.Expired...)

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
		del = capOldest(del, blastCap, olderDeploy)
	}
	plan.Delete = del
	for _, d := range del {
		plan.TotalBytes += d.Bytes
	}
	return plan
}
