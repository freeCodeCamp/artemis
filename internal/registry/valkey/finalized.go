package valkey

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func finalizedKey(site sitekey.Slug, deployID string) string {
	return "deploy:finalized:" + string(site) + "/" + deployID
}

func (s *Store) MarkDeployFinalized(ctx context.Context, site sitekey.Slug, deployID string, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("valkey: mark deploy finalized %s/%s: ttl must be positive, got %s", site, deployID, ttl)
	}
	if err := s.client.Set(ctx, finalizedKey(site, deployID), "1", ttl).Err(); err != nil {
		return fmt.Errorf("valkey: mark deploy finalized %s/%s: %w", site, deployID, err)
	}
	return nil
}

func (s *Store) IsDeployFinalized(ctx context.Context, site sitekey.Slug, deployID string) (bool, error) {
	err := s.client.Get(ctx, finalizedKey(site, deployID)).Err()
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, redis.Nil):
		return false, nil
	default:
		return false, fmt.Errorf("valkey: is deploy finalized %s/%s: %w", site, deployID, err)
	}
}
