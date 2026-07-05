package handler

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"sort"

	"github.com/freeCodeCamp/artemis/internal/auth"
	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/freeCodeCamp/artemis/internal/registry"
)

// fakeRegistry implements RegistryWriter in-memory. Tests pre-seed
// existing slugs via the bySite map; Register adds rows + returns
// ErrAlreadyExists on duplicate. The injected clock keeps timestamps
// deterministic.
type fakeRegistry struct {
	bySite map[string]registry.Site

	// fixedNow drives created_at / updated_at; if zero, time.Now() is used.
	fixedNow time.Time
	// registerErr forces Register to return this error on the next call.
	registerErr error
	getErr      error
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{bySite: map[string]registry.Site{}}
}

func (f *fakeRegistry) Register(_ context.Context, slug string, teams []string, createdBy string) (registry.Site, error) {
	if f.registerErr != nil {
		return registry.Site{}, f.registerErr
	}
	if _, ok := f.bySite[slug]; ok {
		return registry.Site{}, registry.ErrAlreadyExists
	}
	now := f.fixedNow
	if now.IsZero() {
		now = time.Now().UTC()
	}
	teamsCopy := make([]string, len(teams))
	copy(teamsCopy, teams)
	site := registry.Site{
		Slug:      slug,
		Teams:     teamsCopy,
		CreatedAt: now,
		UpdatedAt: now,
		CreatedBy: createdBy,
	}
	f.bySite[slug] = site
	return site, nil
}

func (f *fakeRegistry) UpdateTeams(_ context.Context, slug string, teams []string) (registry.Site, error) {
	if f.registerErr != nil {
		return registry.Site{}, f.registerErr
	}
	existing, ok := f.bySite[slug]
	if !ok {
		return registry.Site{}, registry.ErrNotFound
	}
	now := f.fixedNow
	if now.IsZero() {
		now = time.Now().UTC()
	}
	teamsCopy := make([]string, len(teams))
	copy(teamsCopy, teams)
	updated := registry.Site{
		Slug:      slug,
		Teams:     teamsCopy,
		CreatedAt: existing.CreatedAt,
		UpdatedAt: now,
		CreatedBy: existing.CreatedBy,
	}
	f.bySite[slug] = updated
	return updated, nil
}

func (f *fakeRegistry) Delete(_ context.Context, slug string) error {
	if f.registerErr != nil {
		return f.registerErr
	}
	if _, ok := f.bySite[slug]; !ok {
		return registry.ErrNotFound
	}
	delete(f.bySite, slug)
	return nil
}

func (f *fakeRegistry) GetSite(_ context.Context, slug string) (registry.Site, error) {
	if f.getErr != nil {
		return registry.Site{}, f.getErr
	}
	site, ok := f.bySite[slug]
	if !ok {
		return registry.Site{}, registry.ErrNotFound
	}
	dup := make([]string, len(site.Teams))
	copy(dup, site.Teams)
	site.Teams = dup
	return site, nil
}

func (f *fakeRegistry) Sites(_ context.Context) ([]registry.Site, error) {
	out := make([]registry.Site, 0, len(f.bySite))
	for _, s := range f.bySite {
		// Defensive copy of teams so caller mutations don't leak.
		dup := make([]string, len(s.Teams))
		copy(dup, s.Teams)
		s.Teams = dup
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

// erroringRegistry returns the same error from every method. Used by
// tests that need to assert the handler's error envelope mapping.
type erroringRegistry struct{ err error }

func (e *erroringRegistry) Register(_ context.Context, _ string, _ []string, _ string) (registry.Site, error) {
	return registry.Site{}, e.err
}
func (e *erroringRegistry) UpdateTeams(_ context.Context, _ string, _ []string) (registry.Site, error) {
	return registry.Site{}, e.err
}
func (e *erroringRegistry) Delete(_ context.Context, _ string) error {
	return e.err
}
func (e *erroringRegistry) Sites(_ context.Context) ([]registry.Site, error) {
	return nil, e.err
}
func (e *erroringRegistry) GetSite(_ context.Context, _ string) (registry.Site, error) {
	return registry.Site{}, e.err
}

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

	// userTeamsCalls counts batched /user/teams probes. WhoAmI must
	// hit this at most once per cold cache, never N×.
	userTeamsCalls int
	// authorizeCalls counts AuthorizeForSite invocations. The WhoAmI
	// handler must NOT call AuthorizeForSite at all (intersect
	// locally against the batched UserTeams response instead).
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

// fakeSites implements SitesProvider over an in-memory map.
type fakeSites struct {
	bySite map[string][]string
}

func (f *fakeSites) Snapshot() registry.Snapshot {
	cp := make(map[string][]string, len(f.bySite))
	for k, v := range f.bySite {
		dup := make([]string, len(v))
		copy(dup, v)
		cp[k] = dup
	}
	return staticSnapshot{bySite: cp}
}

// staticSnapshot is a registry.Snapshot impl backed by an in-memory
// map. Test-only — production reads come from valkey.Reader.
type staticSnapshot struct {
	bySite map[string][]string
}

func (s staticSnapshot) Sites() []string {
	out := make([]string, 0, len(s.bySite))
	for k := range s.bySite {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s staticSnapshot) TeamsForSite(slug string) []string {
	teams, ok := s.bySite[slug]
	if !ok {
		return nil
	}
	out := make([]string, len(teams))
	copy(out, teams)
	return out
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
	// the cheaper probe was used.
	hasPrefixCalls  int
	listPrefixCalls int

	// getAliasKeys records the keys passed to GetAlias in call order.
	// Direct-write promote (#28) + rollback CAS (#29) tests use this to
	// assert which alias keys were/weren't probed.
	getAliasKeys []string
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
// Content-Type-propagation assertions. Reuses fakeR2 storage semantics.
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
	f.getAliasKeys = append(f.getAliasKeys, aliasKey)
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

func (f *fakeR2) MovePrefix(ctx context.Context, src, dst string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return 0, f.listErr
	}
	var moved int
	for k, v := range f.objects {
		if hasPrefix(k, src) {
			f.objects[dst+trimPrefix(k, src)] = v
			delete(f.objects, k)
			moved++
		}
	}
	return moved, nil
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

func (f *fakeR2) PrefixBytes(_ context.Context, prefix string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return 0, f.listErr
	}
	var total int64
	for k, v := range f.objects {
		if hasPrefix(k, prefix) {
			total += int64(len(v))
		}
	}
	return total, nil
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
	reg := newFakeRegistry()
	for slug, teams := range st.bySite {
		_, _ = reg.Register(context.Background(), slug, teams, "test")
	}
	h := &Handlers{
		GH:                 gh,
		JWT:                jwt,
		Sites:              st,
		Registry:           reg,
		R2:                 store,
		AliasProductionFmt: "<site>/production",
		AliasPreviewFmt:    "<site>/preview",
		DeployPrefix:       mustDeployPrefixTemplate("<site>/deploys/<ts>-<sha>/"),
		RegistryAuthzTeam:  "staff",
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
