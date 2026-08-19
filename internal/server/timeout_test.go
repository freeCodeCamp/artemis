package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/auth"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type stubSnapshot struct{}

func (stubSnapshot) Sites() []sitekey.Slug              { return []sitekey.Slug{"www"} }
func (stubSnapshot) TeamsForSite(sitekey.Slug) []string { return []string{"team-eng"} }

type stubSites struct{}

func (stubSites) Snapshot() registry.Snapshot { return stubSnapshot{} }

type blockingR2 struct{}

func (blockingR2) PutObject(ctx context.Context, _ string, _ io.Reader, _ string, _ int64) error {
	<-ctx.Done()
	return ctx.Err()
}
func (blockingR2) PutAlias(context.Context, string, string) error       { return nil }
func (blockingR2) GetAlias(context.Context, string) (string, error)     { return "", nil }
func (blockingR2) ListPrefix(context.Context, string) ([]string, error) { return nil, nil }
func (blockingR2) HasPrefix(context.Context, string) (bool, error)      { return false, nil }
func (blockingR2) HasObject(context.Context, string) (bool, error)      { return false, nil }
func (blockingR2) VerifyDeployComplete(context.Context, string, []string) error {
	return nil
}
func (blockingR2) MovePrefix(context.Context, string, string) (int, error) { return 0, nil }
func (blockingR2) PrefixBytes(context.Context, string) (int64, error)      { return 0, nil }

func TestRouter_UploadCarriesADeadline(t *testing.T) {
	signer, err := auth.NewDeploySessionSigner("0123456789abcdef0123456789abcdef", 15*time.Minute)
	require.NoError(t, err)
	tok, _, err := signer.Sign("alice", "www", "20260101-000000-abc1234")
	require.NoError(t, err)

	h := &handler.Handlers{
		JWT:          signer,
		Sites:        stubSites{},
		R2:           blockingR2{},
		DeployPrefix: mustTemplate(t),
		Now:          time.Now,
	}
	r := newWithUploadTimeout(h, 50*time.Millisecond)

	req := httptest.NewRequest(http.MethodPut, "/api/deploy/20260101-000000-abc1234/upload?path=index.html", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() { r.ServeHTTP(w, req); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("upload hung: the deploy-session JWT group had no request timeout at all, so a stalled " +
			"R2 put held the goroutine and connection for as long as the client stayed connected")
	}
	assert.NotEqual(t, http.StatusOK, w.Code)
}

func mustTemplate(t *testing.T) handler.DeployPrefixTemplate {
	t.Helper()
	tpl, err := handler.NewDeployPrefixTemplate("<site>/deploys/<ts>-<sha>/")
	require.NoError(t, err)
	return tpl
}
