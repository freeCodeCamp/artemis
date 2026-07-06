package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/freeCodeCamp/artemis/internal/telemetry"
)

const (
	githubJSON     = "application/vnd.github+json"
	apiVersion     = "2022-11-28"
	listPageSize   = 100
	listMaxPages   = 20
	tokenSafetyPad = 60 * time.Second
	defaultAPIBase = "https://api.github.com"
	// templatesCacheTTL bounds how long an accessible-template list is
	// reused. /api/repo/templates is open to any bearer and each miss
	// probes /contents/ per candidate after paginating org repos, so a
	// short cache caps the App's outbound rate-limit burn.
	templatesCacheTTL = 5 * time.Minute
)

var reCouldNotClone = regexp.MustCompile(`(?i)could not clone`)

// UserFacingError carries a message that is safe to surface to the
// approving admin: curated GitHub copy with no internal endpoints,
// installation tokens, or transport details. CreateRepo returns it on
// the approved_failed path; every other error it returns is internal
// and must be kept opaque at the HTTP boundary.
type UserFacingError struct {
	Msg       string
	Retryable bool
}

func (e *UserFacingError) Error() string { return e.Msg }

func IsTransient(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var uf *UserFacingError
	return errors.As(err, &uf) && uf.Retryable
}

func userFacingf(format string, args ...any) error {
	return &UserFacingError{Msg: fmt.Sprintf(format, args...)}
}

// RepoExistsError reports that the target repo already exists in the org.
// On a fresh approval the handler surfaces it to the admin
// (approved_failed); on an approve-retry of a row stranded in `approved`
// it is the prior creation, and the handler reconciles to active via URL.
type RepoExistsError struct {
	Org, Name, URL string
}

func (e *RepoExistsError) Error() string {
	return fmt.Sprintf("repository %s/%s already exists", e.Org, e.Name)
}

// ClientConfig configures the GitHub App REST client.
type ClientConfig struct {
	APIBase        string // default https://api.github.com
	Org            string // target org for repo creation (freeCodeCamp-Universe)
	InstallationID string
	Signer         *AppJWTSigner
	HTTPClient     *http.Client // optional override (testability)
	Now            func() time.Time
}

// Client drives the Apollo-11 App's repo-creation REST calls. It caches
// the installation access token until shortly before its expiry.
type Client struct {
	httpClient     *http.Client
	apiBase        string
	org            string
	installationID string
	signer         *AppJWTSigner
	now            func() time.Time

	mu       sync.Mutex
	token    string
	tokenExp time.Time

	templatesMu    sync.Mutex
	templatesVal   []string
	templatesValid bool
	templatesExp   time.Time
}

// NewClient validates config and returns a ready Client.
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.Signer == nil {
		return nil, errors.New("githubapp: nil signer")
	}
	if cfg.InstallationID == "" {
		return nil, errors.New("githubapp: empty installation id")
	}
	if cfg.Org == "" {
		return nil, errors.New("githubapp: empty org")
	}
	apiBase := cfg.APIBase
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second, Transport: telemetry.NewRoundTripper(nil)}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		httpClient:     hc,
		apiBase:        strings.TrimRight(apiBase, "/"),
		org:            cfg.Org,
		installationID: cfg.InstallationID,
		signer:         cfg.Signer,
		now:            now,
	}, nil
}

// CreateSpec is the input to CreateRepo. Template empty ⇒ blank repo.
type CreateSpec struct {
	Name        string
	Private     bool
	Description string
	Template    string
}

// Created is the successful outcome of CreateRepo.
type Created struct {
	FullName   string
	URL        string
	Visibility string
}

// installationToken returns a cached or freshly minted installation
// access token. The token is minted by exchanging a short-lived App JWT
// at POST /app/installations/{id}/access_tokens.
func (c *Client) installationToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.token != "" && c.now().Before(c.tokenExp.Add(-tokenSafetyPad)) {
		tok := c.token
		c.mu.Unlock()
		return tok, nil
	}
	c.mu.Unlock()

	jwtStr, err := c.signer.Sign()
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/app/installations/%s/access_tokens", c.apiBase, c.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", fmt.Errorf("githubapp: build token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwtStr)
	req.Header.Set("Accept", githubJSON)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("githubapp: token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		if msg := parseGitHubMessage(body); msg != "" {
			return "", fmt.Errorf("githubapp: installation token failed: status %d: %s", resp.StatusCode, msg)
		}
		return "", fmt.Errorf("githubapp: installation token failed: status %d", resp.StatusCode)
	}
	var data struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("githubapp: parse installation token: %w", err)
	}
	if data.Token == "" {
		return "", errors.New("githubapp: empty installation token")
	}

	c.mu.Lock()
	c.token = data.Token
	if data.ExpiresAt.IsZero() {
		c.tokenExp = c.now().Add(tokenSafetyPad)
	} else {
		c.tokenExp = data.ExpiresAt
	}
	c.mu.Unlock()
	return data.Token, nil
}

// CreateRepo creates a repo in the org — blank (POST /orgs/{org}/repos)
// or from a template (POST /repos/{org}/{template}/generate). It checks
// for an existing repo first, and disables Actions on private repos.
// On failure it returns a *UserFacingError when the message is safe to
// surface to the approving admin (the approved_failed path); any other
// error is internal (token mint, existence probe, transport, parse) and
// the handler keeps it opaque at the HTTP boundary.
func (c *Client) CreateRepo(ctx context.Context, spec CreateSpec) (Created, error) {
	token, err := c.installationToken(ctx)
	if err != nil {
		return Created{}, err
	}

	exists, existingURL, err := c.repoExists(ctx, token, spec.Name)
	if err != nil {
		return Created{}, err
	}
	if exists {
		return Created{}, &RepoExistsError{Org: c.org, Name: spec.Name, URL: existingURL}
	}

	var url string
	var reqBody []byte
	if spec.Template != "" {
		url = fmt.Sprintf("%s/repos/%s/%s/generate", c.apiBase, c.org, spec.Template)
		reqBody, _ = json.Marshal(map[string]any{
			"owner":                c.org,
			"name":                 spec.Name,
			"description":          spec.Description,
			"private":              spec.Private,
			"include_all_branches": false,
		})
	} else {
		url = fmt.Sprintf("%s/orgs/%s/repos", c.apiBase, c.org)
		reqBody, _ = json.Marshal(map[string]any{
			"name":                   spec.Name,
			"description":            spec.Description,
			"private":                spec.Private,
			"auto_init":              true,
			"has_issues":             true,
			"has_projects":           false,
			"has_wiki":               false,
			"delete_branch_on_merge": true,
		})
	}

	resp, body, err := c.do(ctx, http.MethodPost, url, token, reqBody)
	if err != nil {
		return Created{}, err
	}
	if resp.StatusCode != http.StatusCreated {
		return Created{}, formatGitHubError(resp.StatusCode, body, spec.Template)
	}

	var repo struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
		Private  bool   `json:"private"`
	}
	if err := json.Unmarshal(body, &repo); err != nil {
		return Created{}, fmt.Errorf("githubapp: parse created repo: %w", err)
	}

	if spec.Private {
		c.disableActions(ctx, token, spec.Name)
	}

	vis := "public"
	if repo.Private {
		vis = "private"
	}
	return Created{FullName: repo.FullName, URL: repo.HTMLURL, Visibility: vis}, nil
}

func (c *Client) RepoExists(ctx context.Context, name string) (bool, string, error) {
	token, err := c.installationToken(ctx)
	if err != nil {
		return false, "", err
	}
	return c.repoExists(ctx, token, name)
}

func (c *Client) repoExists(ctx context.Context, token, name string) (bool, string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s", c.apiBase, c.org, name)
	resp, body, err := c.do(ctx, http.MethodGet, url, token, nil)
	if err != nil {
		return false, "", err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var repo struct {
			HTMLURL string `json:"html_url"`
		}
		_ = json.Unmarshal(body, &repo)
		return true, repo.HTMLURL, nil
	case http.StatusNotFound:
		return false, "", nil
	default:
		if msg := parseGitHubMessage(body); msg != "" {
			return false, "", fmt.Errorf("githubapp: existence check status %d: %s", resp.StatusCode, msg)
		}
		return false, "", fmt.Errorf("githubapp: existence check status %d", resp.StatusCode)
	}
}

// disableActions is best-effort: a private repo created with Actions
// enabled is a soft policy miss, not a creation failure, so a non-OK
// status is logged, not returned.
func (c *Client) disableActions(ctx context.Context, token, name string) {
	url := fmt.Sprintf("%s/repos/%s/%s/actions/permissions", c.apiBase, c.org, name)
	body, _ := json.Marshal(map[string]any{"enabled": false})
	resp, respBody, err := c.do(ctx, http.MethodPut, url, token, body)
	if err != nil {
		slog.WarnContext(ctx, "githubapp.disable_actions.failed", "repo", name, "err", err)
		return
	}
	if resp.StatusCode >= 300 {
		slog.WarnContext(ctx, "githubapp.disable_actions.non_2xx", "repo", name, "status", resp.StatusCode, "ghMessage", parseGitHubMessage(respBody))
	}
}

// ListTemplates lists org repos with is_template=true that the App can
// actually clone (Contents:read), sorted. Paginates the org-repos
// endpoint, stopping on a short page or at listMaxPages. Returns an
// error on any GitHub failure; the handler decides whether to fail-soft
// to an empty list at the HTTP boundary.
func (c *Client) ListTemplates(ctx context.Context) ([]string, error) {
	c.templatesMu.Lock()
	if c.templatesValid && c.now().Before(c.templatesExp) {
		cached := append([]string(nil), c.templatesVal...)
		c.templatesMu.Unlock()
		return cached, nil
	}
	c.templatesMu.Unlock()

	token, err := c.installationToken(ctx)
	if err != nil {
		return nil, err
	}

	var candidates []string
	for page := 1; page <= listMaxPages; page++ {
		url := fmt.Sprintf("%s/orgs/%s/repos?type=all&per_page=%d&page=%d",
			c.apiBase, c.org, listPageSize, page)
		resp, body, err := c.do(ctx, http.MethodGet, url, token, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			if msg := parseGitHubMessage(body); msg != "" {
				return nil, fmt.Errorf("githubapp: list repos status %d: %s", resp.StatusCode, msg)
			}
			return nil, fmt.Errorf("githubapp: list repos status %d", resp.StatusCode)
		}
		var repos []struct {
			Name       string `json:"name"`
			IsTemplate bool   `json:"is_template"`
		}
		if err := json.Unmarshal(body, &repos); err != nil {
			return nil, fmt.Errorf("githubapp: parse repos: %w", err)
		}
		for _, r := range repos {
			if r.IsTemplate && r.Name != "" {
				candidates = append(candidates, r.Name)
			}
		}
		if len(repos) < listPageSize {
			break
		}
	}

	accessible := make([]string, 0, len(candidates))
	for _, name := range candidates {
		if c.isAccessible(ctx, token, name) {
			accessible = append(accessible, name)
		}
	}
	sort.Strings(accessible)

	c.templatesMu.Lock()
	c.templatesVal = append([]string(nil), accessible...)
	c.templatesValid = true
	c.templatesExp = c.now().Add(templatesCacheTTL)
	c.templatesMu.Unlock()

	return accessible, nil
}

func (c *Client) isAccessible(ctx context.Context, token, name string) bool {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/", c.apiBase, c.org, name)
	resp, _, err := c.do(ctx, http.MethodGet, url, token, nil)
	if err != nil {
		return false
	}
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// do issues an authenticated GitHub REST request with the installation
// token and returns the response (body already drained + closed) plus
// the raw body bytes.
func (c *Client) do(ctx context.Context, method, url, token string, body []byte) (*http.Response, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, nil, fmt.Errorf("githubapp: build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", githubJSON)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("githubapp: %s: %w", method, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	return resp, rb, nil
}

func parseGitHubMessage(body []byte) string {
	var p struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return ""
	}
	return p.Message
}

// formatGitHubError maps a non-201 repo-create response to a user-facing
// error. Ported from the Windmill flow (f/github/create_repo.ts) so the
// admin sees the same actionable copy the Chat flow surfaced.
func formatGitHubError(status int, body []byte, template string) error {
	msg := parseGitHubMessage(body)
	switch {
	case status == http.StatusUnprocessableEntity && template != "" && msg != "" && reCouldNotClone.MatchString(msg):
		return userFacingf("template %q is not accessible to the Apollo-11 GitHub App (missing Contents:read); ask an org admin to grant the App access to the template repo", template)
	case status == http.StatusForbidden:
		if msg != "" {
			return userFacingf("Apollo-11 GitHub App lacks permission: %s", msg)
		}
		return &UserFacingError{Msg: "Apollo-11 GitHub App lacks permission; contact an org admin"}
	case status >= 500:
		return &UserFacingError{Msg: "GitHub API temporarily unavailable; please retry shortly", Retryable: true}
	default:
		if msg != "" {
			return userFacingf("GitHub API error (%d): %s", status, msg)
		}
		return userFacingf("GitHub API error (%d)", status)
	}
}
