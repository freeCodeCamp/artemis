package teamcache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const keyPrefix = "ghteams:"

type Cache struct {
	client *redis.Client
	ttl    time.Duration
}

func New(client *redis.Client, ttl time.Duration) *Cache {
	return &Cache{client: client, ttl: ttl}
}

func key(login string) string { return keyPrefix + login }

func (c *Cache) Get(ctx context.Context, login string) ([]string, bool, error) {
	raw, err := c.client.Get(ctx, key(login)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("teamcache get %s: %w", login, err)
	}
	var teams []string
	if err := json.Unmarshal([]byte(raw), &teams); err != nil {
		return nil, false, fmt.Errorf("teamcache decode %s: %w", login, err)
	}
	return teams, true, nil
}

func (c *Cache) Set(ctx context.Context, login string, teams []string) error {
	if teams == nil {
		teams = []string{}
	}
	b, err := json.Marshal(teams)
	if err != nil {
		return fmt.Errorf("teamcache encode %s: %w", login, err)
	}
	if err := c.client.Set(ctx, key(login), b, c.ttl).Err(); err != nil {
		return fmt.Errorf("teamcache set %s: %w", login, err)
	}
	return nil
}

func (c *Cache) GetOrFetch(ctx context.Context, login string, fetch func(ctx context.Context) ([]string, error)) ([]string, error) {
	if teams, hit, err := c.Get(ctx, login); err != nil {
		return nil, err
	} else if hit {
		return teams, nil
	}
	teams, err := fetch(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.Set(ctx, login, teams); err != nil {
		return nil, err
	}
	return teams, nil
}
