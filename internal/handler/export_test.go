package handler

import "sync"

func resetMetricsForTest() {
	pkgMetrics = nil
	pkgMetricsOnce = sync.Once{}
}
