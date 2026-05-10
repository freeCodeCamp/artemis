package handler

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/auth"
	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sites"
)

// fakeGH implements GitHubAuthenticator with deterministic behaviour.
//
//   - tokenLogins maps Bearer token → resolved login (covers ValidateToken)
//   - userTeams   maps login        → set of team slugs the user belongs to
//
// AuthorizeForSite reports true when the intersection of the user's teams
// and the site's authorized teams is non-empty. This mirrors the real
// client's "any-team grants" semantics.
type fakeGH struct {
	tokenLogins map[string]string
	userTeams   map[string]map[string]bool
	upstreamErr error

	// userTeamsCalls counts batched /user/teams probes (B9). WhoAmI
	// must hit this at most once per cold cache, never N×.
	userTeamsCalls int
	// authorizeCalls counts AuthorizeForSite invocations. Post-B9 the
	// WhoAmI handler must NOT call AuthorizeForSite at all (intersect
	// locally instead).
	authorizeCalls int
}

func (f *fakeGH) ValidateToken(_ context.Context, token string) (string, error) {
	if f.upstreamErr != nil {
		return "", f.upstreamErr
	}
	if login, ok := f.tokenLogins[token]; ok {
		return login, nil
	}
	return "", auth.ErrGitHubUnauthenticated
}

func (f *fakeGH) AuthorizeForSite(_ context.Context, _ string, login string, teams []string) (bool, error) {
	f.authorizeCalls++
	if f.upstreamErr != nil {
		return false, f.upstreamErr
	}
	mem := f.userTeams[login]
	for _, t := range teams {
		if mem[t] {
			return true, nil
		}
	}
	return false, nil
}

// UserTeams returns the slugs the resolved login belongs to. Tracked by
// userTeamsCalls so B9 tests can assert one cold-cache call per request.
func (f *fakeGH) UserTeams(_ context.Context, token string) ([]string, error) {
	f.userTeamsCalls++
	if f.upstreamErr != nil {
		return nil, f.upstreamErr
	}
	login, ok := f.tokenLogins[token]
	if !ok {
		return nil, auth.ErrGitHubUnauthenticated
	}
	mem := f.userTeams[login]
	out := make([]string, 0, len(mem))
	for slug, member := range mem {
		if member {
			out = append(out, slug)
		}
	}
	return out, nil
}

// fakeJWT implements DeployJWTSigner with a real signer wrapped to keep
// tests independent of the concrete struct.
type fakeJWT struct {
	signer *auth.DeploySessionSigner
}

func newFakeJWT(t *testing.T) *fakeJWT {
	t.Helper()
	s, err := auth.NewDeploySessionSigner("0123456789abcdef0123456789abcdef", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeJWT{signer: s}
}

// newShortLivedSigner returns a JWT signer with a 1-millisecond TTL —
// useful for asserting the expired-token branch of RequireDeployJWT.
func newShortLivedSigner() (*fakeJWT, error) {
	s, err := auth.NewDeploySessionSigner("0123456789abcdef0123456789abcdef", time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &fakeJWT{signer: s}, nil
}

// sleepUntilExpired waits long enough that a 1ms-TTL JWT is guaranteed expired.
func sleepUntilExpired() {
	time.Sleep(20 * time.Millisecond)
}

func (f *fakeJWT) Sign(login, site, deployID string) (string, time.Time, error) {
	return f.signer.Sign(login, site, deployID)
}

func (f *fakeJWT) Verify(token string) (auth.DeploySessionClaims, error) {
	return f.signer.Verify(token)
}

// fakeSites implements SitesProvider over an in-memory map. The
// returned snapshot is the concrete sites.Snapshot — that struct
// already satisfies registry.Snapshot via its Sites/TeamsForSite
// methods, so the test-side fake stays the same shape post-refactor.
type fakeSites struct {
	bySite map[string][]string
}

func (f *fakeSites) Snapshot() registry.Snapshot {
	return sites.NewSnapshot(f.bySite)
}

// fakeR2 implements R2Store in-memory. It tracks the set of stored keys
// and the alias contents, with hooks to inject errors.
type fakeR2 struct {
	mu      sync.Mutex
	objects map[string][]byte
	aliases map[string]string

	listErr   error
	putErr    error
	verifyErr error

	// hasPrefixCalls and listPrefixCalls let SiteRollback tests assert
	// the cheaper probe was used (B6).
	hasPrefixCalls  int
	listPrefixCalls int
}

func newFakeR2() *fakeR2 {
	return &fakeR2{
		objects: make(map[string][]byte),
		aliases: make(map[string]string),
	}
}

func (f *fakeR2) PutObject(_ context.Context, key string, body io.Reader, _ string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.objects[key] = b
	return nil
}

// recordingFakeR2 captures the Content-Type passed to PutObject for
// B23 assertions. Reuses fakeR2 storage semantics.
type recordingFakeR2 struct {
	*fakeR2
	lastContentType string
}

func (f *recordingFakeR2) PutObject(ctx context.Context, key string, body io.Reader, contentType string, contentLength int64) error {
	f.lastContentType = contentType
	return f.fakeR2.PutObject(ctx, key, body, contentType, contentLength)
}

func (f *fakeR2) PutAlias(_ context.Context, aliasKey, deployID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	f.aliases[aliasKey] = deployID
	return nil
}

func (f *fakeR2) GetAlias(_ context.Context, aliasKey string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.aliases[aliasKey]
	if !ok {
		return "", r2.ErrNotFound
	}
	return v, nil
}

func (f *fakeR2) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listPrefixCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []string
	for k := range f.objects {
		if hasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (f *fakeR2) HasPrefix(_ context.Context, prefix string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hasPrefixCalls++
	if f.listErr != nil {
		return false, f.listErr
	}
	for k := range f.objects {
		if hasPrefix(k, prefix) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeR2) VerifyDeployComplete(_ context.Context, prefix string, expected []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.verifyErr != nil {
		return f.verifyErr
	}
	have := map[string]struct{}{}
	for k := range f.objects {
		if hasPrefix(k, prefix) {
			have[trimPrefix(k, prefix)] = struct{}{}
		}
	}
	var missing []string
	for _, w := range expected {
		if _, ok := have[w]; !ok {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		return &r2.VerifyError{Prefix: prefix, Missing: missing}
	}
	return nil
}

func hasPrefix(s, p string) bool {
	if len(s) < len(p) {
		return false
	}
	return s[:len(p)] == p
}

func trimPrefix(s, p string) string {
	if hasPrefix(s, p) {
		return s[len(p):]
	}
	return s
}

// newTestHandlers wires a Handlers struct with the fakes plus sensible
// alias/prefix templates.
func newTestHandlers(t *testing.T, gh *fakeGH, st *fakeSites, store R2Store) (*Handlers, *fakeJWT) {
	t.Helper()
	jwt := newFakeJWT(t)
	h := &Handlers{
		GH:                 gh,
		JWT:                jwt,
		Sites:              st,
		R2:                 store,
		AliasProductionFmt: "<site>/production",
		AliasPreviewFmt:    "<site>/preview",
		DeployPrefix:       mustDeployPrefixTemplate("<site>/deploys/<ts>-<sha>/"),
		NewDeployID: func(sha string) string {
			return "20260420-141522-" + sha[:min(7, len(sha))]
		},
		Now: time.Now,
		PublicURLForSite: func(site, mode string) string {
			if mode == "production" {
				return "https://" + site + ".freecode.camp"
			}
			return "https://" + site + ".preview.freecode.camp"
		},
	}
	return h, jwt
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// mustDeployPrefixTemplate panics if the literal raw cannot be parsed.
// Test-only helper — production wiring uses NewDeployPrefixTemplate
// with explicit error handling.
func mustDeployPrefixTemplate(raw string) DeployPrefixTemplate {
	tpl, err := NewDeployPrefixTemplate(raw)
	if err != nil {
		panic(err)
	}
	return tpl
}
