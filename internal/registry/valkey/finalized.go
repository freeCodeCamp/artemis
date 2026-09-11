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

func finalizedModeKey(site sitekey.Slug, deployID, mode string) string {
	return finalizedKey(site, deployID) + "/" + mode
}

func (s *Store) MarkDeployFinalized(ctx context.Context, site sitekey.Slug, deployID, mode string, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("valkey: mark deploy finalized %s/%s: ttl must be positive, got %s", site, deployID, ttl)
	}
	tx := s.client.TxPipeline()
	tx.Set(ctx, finalizedKey(site, deployID), "1", ttl)
	tx.Set(ctx, finalizedModeKey(site, deployID, mode), "1", ttl)
	if _, err := tx.Exec(ctx); err != nil {
		return fmt.Errorf("valkey: mark deploy finalized %s/%s/%s: %w", site, deployID, mode, err)
	}
	return nil
}

func (s *Store) IsDeployModeFinalized(ctx context.Context, site sitekey.Slug, deployID, mode string) (bool, error) {
	err := s.client.Get(ctx, finalizedModeKey(site, deployID, mode)).Err()
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, redis.Nil):
		return false, nil
	default:
		return false, fmt.Errorf("valkey: is deploy mode finalized %s/%s/%s: %w", site, deployID, mode, err)
	}
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
