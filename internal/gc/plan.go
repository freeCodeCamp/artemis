package gc

import "fmt"

type Plan struct {
	Site       string
	Delete     []Deploy
	TotalBytes int64
	Aborted    bool
	Reason     string
}

func PlanSite(site string, in RetainInput, p Policy, blastCap int) Plan {
	_, del := Retain(in, p)

	var total int64
	for _, d := range del {
		total += d.Bytes
	}

	plan := Plan{Site: site, Delete: del, TotalBytes: total}
	if blastCap > 0 && len(del) > blastCap {
		plan.Aborted = true
		plan.Reason = fmt.Sprintf("delete plan of %d exceeds blast-cap %d", len(del), blastCap)
		plan.Delete = nil
		plan.TotalBytes = 0
	}
	return plan
}
