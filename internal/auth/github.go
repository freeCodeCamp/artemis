package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GitHubClientConfig configures the GitHub REST client used for identity
// validation and team-membership probes.
type GitHubClientConfig struct {
	APIBase    string        // default https://api.github.com
	Org        string        // GitHub org slug (default freeCodeCamp)
	CacheTTL   time.Duration // membership + identity cache TTL (default 5 min)
	HTTPClient *http.Client  // optional override (testability)
	Now        func() time.Time
}

// GitHubClient validates GitHub bearer tokens against /user and probes
// team memberships in the configured org. Both call paths are cached for
// CacheTTL to absorb steady-state pressure on the GitHub REST API.
type GitHubClient struct {
	cfg        GitHubClientConfig
	httpClient *http.Client
	now        func() time.Time

	mu        sync.Mutex
	userCache map[string]userCacheEntry // token → cached login
	teamCache map[teamCacheKey]teamCacheEntry
}

type userCacheEntry struct {
	login   string
	expires time.Time
}

type teamCacheKey struct {
	user string
	team string
}

type teamCacheEntry struct {
	member  bool
	expires time.Time
}

// NewGitHubClient returns a configured client. Defaults: api.github.com,
// 5-minute cache, std http.Client with 10s timeout.
func NewGitHubClient(cfg GitHubClientConfig) *GitHubClient {
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.github.com"
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 5 * time.Minute
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &GitHubClient{
		cfg:        cfg,
		httpClient: cfg.HTTPClient,
		now:        cfg.Now,
		userCache:  make(map[string]userCacheEntry),
		teamCache:  make(map[teamCacheKey]teamCacheEntry),
	}
}

// ValidateToken calls GET /user with the supplied bearer and returns the
// resolved login. Successful results are cached for CacheTTL keyed by
// raw token. Rate-limit and 5xx responses are surfaced via typed errors
// (IsGitHubRateLimited / IsGitHubUnavailable).
func (c *GitHubClient) ValidateToken(ctx context.Context, token string) (string, error) {
	c.mu.Lock()
	if entry, ok := c.userCache[token]; ok && entry.expires.After(c.now()) {
		c.mu.Unlock()
		return entry.login, nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIBase+"/user", nil)
	if err != nil {
		return "", fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	switch {
	case resp.StatusCode == http.StatusOK:
		// fall through
	case resp.StatusCode == http.StatusUnauthorized:
		return "", ErrGitHubUnauthenticated
	case resp.StatusCode == http.StatusForbidden && bodyMentionsRateLimit(body):
		return "", ErrGitHubRateLimited
	case resp.StatusCode == http.StatusForbidden:
		return "", ErrGitHubUnauthenticated
	case resp.StatusCode >= 500:
		return "", ErrGitHubUnavailable
	default:
		return "", fmt.Errorf("github /user: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return "", fmt.Errorf("github /user: parse: %w", err)
	}
	if u.Login == "" {
		return "", errors.New("github /user: empty login")
	}

	c.mu.Lock()
	c.userCache[token] = userCacheEntry{
		login:   u.Login,
		expires: c.now().Add(c.cfg.CacheTTL),
	}
	c.mu.Unlock()

	return u.Login, nil
}

// IsTeamMember returns true if `user` has an active membership on
// `<org>/<team>`. Cached for CacheTTL keyed by (user, team).
func (c *GitHubClient) IsTeamMember(ctx context.Context, token, user, teamSlug string) (bool, error) {
	key := teamCacheKey{user: user, team: teamSlug}

	c.mu.Lock()
	if entry, ok := c.teamCache[key]; ok && entry.expires.After(c.now()) {
		c.mu.Unlock()
		return entry.member, nil
	}
	c.mu.Unlock()

	url := fmt.Sprintf("%s/orgs/%s/teams/%s/memberships/%s",
		c.cfg.APIBase, c.cfg.Org, teamSlug, user)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var member bool
	switch {
	case resp.StatusCode == http.StatusOK:
		var m struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(body, &m); err == nil {
			member = m.State == "active"
		}
	case resp.StatusCode == http.StatusNotFound:
		member = false
	case resp.StatusCode == http.StatusForbidden && bodyMentionsRateLimit(body):
		return false, ErrGitHubRateLimited
	case resp.StatusCode >= 500:
		return false, ErrGitHubUnavailable
	default:
		return false, fmt.Errorf("github team membership: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	c.mu.Lock()
	c.teamCache[key] = teamCacheEntry{
		member:  member,
		expires: c.now().Add(c.cfg.CacheTTL),
	}
	c.mu.Unlock()

	return member, nil
}

// AuthorizeForSite returns true iff `user` is an active member of any
// team in `teams`. Empty `teams` → false (no implicit grant).
func (c *GitHubClient) AuthorizeForSite(ctx context.Context, token, user string, teams []string) (bool, error) {
	for _, team := range teams {
		ok, err := c.IsTeamMember(ctx, token, user, team)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// Typed errors used by handlers to map upstream GitHub status to HTTP
// responses (401 / 403 / 429 / 503).
var (
	ErrGitHubUnauthenticated = errors.New("github: unauthenticated")
	ErrGitHubRateLimited     = errors.New("github: rate limited")
	ErrGitHubUnavailable     = errors.New("github: upstream unavailable")
)

// IsGitHubRateLimited reports whether err originates from a GitHub
// rate-limit response.
func IsGitHubRateLimited(err error) bool { return errors.Is(err, ErrGitHubRateLimited) }

// IsGitHubUnavailable reports whether err originates from a GitHub 5xx.
func IsGitHubUnavailable(err error) bool { return errors.Is(err, ErrGitHubUnavailable) }

// IsGitHubUnauthenticated reports whether err originates from a 401/403
// (non-rate-limited) response.
func IsGitHubUnauthenticated(err error) bool { return errors.Is(err, ErrGitHubUnauthenticated) }

func bodyMentionsRateLimit(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "rate limit")
}
