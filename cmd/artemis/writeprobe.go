package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"time"
)

const (
	opWriteProbe       = "r2.write_probe"
	writeProbeInterval = 15 * time.Minute
	writeProbeTimeout  = 15 * time.Second
	writeProbeKey      = "_probe/write-probe"
)

type writeProber interface {
	PutObject(ctx context.Context, key string, body io.Reader, contentType string, contentLength int64) error
}

func runWriteProbeLoop(ctx context.Context, probe writeProber, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runWriteProbe(ctx, probe)
		}
	}
}

func runWriteProbe(ctx context.Context, probe writeProber) {
	probeCtx, cancel := context.WithTimeout(ctx, writeProbeTimeout)
	defer cancel()
	body := time.Now().UTC().Format(time.RFC3339Nano)
	if err := probe.PutObject(probeCtx, writeProbeKey, strings.NewReader(body), "text/plain", int64(len(body))); err != nil {
		slog.ErrorContext(ctx, "r2.write_probe.failed", "key", writeProbeKey, "err", err,
			"detail", "readyz calls ListObjectsV2 only, so a credential that lost its write grant leaves "+
				"pod state, readyz and every alert green until the next deploy fails")
		captureBackground(opWriteProbe, err)
		return
	}
	slog.DebugContext(ctx, "r2.write_probe.ok", "key", writeProbeKey)
}
