package telemetry_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/telemetry"
)

func BenchmarkLogHandler_NoScope(b *testing.B) {
	log := slog.New(telemetry.NewLogHandler(slog.NewJSONHandler(io.Discard, nil)))
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		log.InfoContext(ctx, "bench.noop")
	}
}

func BenchmarkLogHandler_WithScope(b *testing.B) {
	log := slog.New(telemetry.NewLogHandler(slog.NewJSONHandler(io.Discard, nil)))
	sc := telemetry.New("req-1")
	sc.SetActor("alice")
	ctx := telemetry.NewContext(context.Background(), sc)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		log.InfoContext(ctx, "bench.op")
	}
}
