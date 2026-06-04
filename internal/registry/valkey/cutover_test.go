package valkey_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/registry/valkey"
)

type stubSource struct {
	mu     sync.Mutex
	bySite map[string][]string
}

func newStubSource() *stubSource {
	return &stubSource{bySite: map[string][]string{}}
}

func (s *stubSource) set(slug string, teams []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bySite[slug] = append([]string(nil), teams...)
}

func (s *stubSource) Sites(_ context.Context) ([]registry.Site, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]registry.Site, 0, len(s.bySite))
	for slug, teams := range s.bySite {
		out = append(out, registry.Site{Slug: slug, Teams: append([]string(nil), teams...)})
	}
	return out, nil
}

func TestRegistryCutover(t *testing.T) {
	t.Parallel()

	pubsub, _, _ := newStore(t)
	source := newStubSource()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	source.set("preexisting", []string{"staff"})

	reader, err := valkey.NewReaderFromSource(ctx, source, pubsub, valkey.DefaultRefreshFallback)
	require.NoError(t, err)

	snap := reader.Snapshot()
	require.Equal(t, []string{"preexisting"}, snap.Sites(),
		"initial read served from PG source via cache-front")
	require.Equal(t, []string{"staff"}, snap.TeamsForSite("preexisting"))

	onChange := valkey.PublishOnChange(ctx, pubsub)
	source.set("blog", []string{"news-editors"})
	onChange("blog")

	eventually(t, 2*time.Second, "OnChange publish propagates a PG write into the cache-front", func() bool {
		s := reader.Snapshot()
		return len(s.Sites()) == 2 && s.TeamsForSite("blog") != nil
	})
}
