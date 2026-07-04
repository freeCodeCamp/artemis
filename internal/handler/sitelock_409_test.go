package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeErrLocker struct{ err error }

func (l *fakeErrLocker) WithSiteLock(_ context.Context, _ string, _ func() error) error {
	return l.err
}

func lockTimeoutErr() error {
	return fmt.Errorf("site lock www.freecode.camp: %w",
		&pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"})
}

func TestSiteRollback_LockTimeout_Returns409Retryable(t *testing.T) {
	h, _ := newTestHandlers(t, authedGH(), standardSites(), newFakeR2())
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	h.Locker = &fakeErrLocker{err: lockTimeoutErr()}

	body, _ := json.Marshal(SiteRollbackRequest{To: "20260101-000000-old0001"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"site_locked"`)
}

func TestSitePromote_LockTimeout_Returns409Retryable(t *testing.T) {
	h, _ := newTestHandlers(t, authedGH(), standardSites(), newFakeR2())
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	h.Locker = &fakeErrLocker{err: lockTimeoutErr()}

	body, _ := json.Marshal(SitePromoteRequest{DeployID: "20260420-141522-abc1234"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"site_locked"`)
}

func TestSiteLock_GenericError_Returns502(t *testing.T) {
	h, _ := newTestHandlers(t, authedGH(), standardSites(), newFakeR2())
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	h.Locker = &fakeErrLocker{err: fmt.Errorf("site lock www.freecode.camp: connect: dial tcp: refused")}

	body, _ := json.Marshal(SiteRollbackRequest{To: "20260101-000000-old0001"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"site_lock_failed"`)
}
