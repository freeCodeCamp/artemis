package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const unlockFailDeployID = "20260420-141522-abc1234"

func decodeSingleBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	dec := json.NewDecoder(strings.NewReader(w.Body.String()))
	require.NoError(t, dec.Decode(&body), w.Body.String())
	require.False(t, dec.More(),
		"the closure already answered; a second write concatenates two JSON objects into one body")
	return body
}

func runUnlockFailUpdate(t *testing.T, broken bool) (*httptest.ResponseRecorder, *fakeAudit) {
	t.Helper()
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	h.Locker = unlockErrLocker{}
	registerExample(t, h)
	fa := &fakeAudit{}
	h.Audit = fa
	if broken {
		reg, ok := h.Registry.(*fakeRegistry)
		require.True(t, ok)
		reg.registerErr = errors.New("valkey down")
	}
	body, _ := json.Marshal(SiteUpdateRequest{Teams: []string{"platform"}})
	return callUpdate(h, "example", body, "alice", "tok"), fa
}

func runUnlockFailPurge(t *testing.T, broken bool) (*httptest.ResponseRecorder, *fakeAudit) {
	t.Helper()
	base := newFakeR2()
	base.objects["example/deploys/"+unlockFailDeployID+"/index.html"] = []byte("hi")
	var store R2Store = base
	if broken {
		store = &failBulkMoveR2{fakeR2: base, bulkPrefix: "example/"}
	}
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Locker = unlockErrLocker{}
	h.Tombstones = &fakeTombstones{}
	registerExample(t, h)
	fa := &fakeAudit{}
	h.Audit = fa
	return callPurgeSlug(h, "example"), fa
}

func runUnlockFailDeployDelete(t *testing.T, broken bool) (*httptest.ResponseRecorder, *fakeAudit) {
	t.Helper()
	store := newFakeR2()
	store.objects["www/deploys/"+unlockFailDeployID+"/index.html"] = []byte("hi")
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Locker = unlockErrLocker{}
	h.Tombstones = &fakeTombstones{}
	fa := &fakeAudit{}
	h.Audit = fa
	if broken {
		store.listErr = errors.New("r2 move outage")
	}
	return callDeployDelete(h, "www", unlockFailDeployID), fa
}

func runUnlockFailDeployRestore(t *testing.T, broken bool) (*httptest.ResponseRecorder, *fakeAudit) {
	t.Helper()
	store := newFakeR2()
	store.objects["_trash/www/"+unlockFailDeployID+"/index.html"] = []byte("hi")
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Locker = unlockErrLocker{}
	h.Trash = &fakeTrash{}
	fa := &fakeAudit{}
	h.Audit = fa
	if broken {
		store.listErr = errors.New("r2 move outage")
	}
	return callDeployRestore(h, "www", unlockFailDeployID), fa
}

func runUnlockFailPromote(t *testing.T, broken bool) (*httptest.ResponseRecorder, *fakeAudit) {
	t.Helper()
	store := newFakeR2()
	store.objects["www/deploys/"+unlockFailDeployID+"/index.html"] = []byte("hi")
	store.aliases["www/preview"] = unlockFailDeployID
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Locker = unlockErrLocker{}
	h.Index = &fakeIndex{fail: broken}
	fa := &fakeAudit{}
	h.Audit = fa
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	return w, fa
}

func runUnlockFailRollback(t *testing.T, broken bool) (*httptest.ResponseRecorder, *fakeAudit) {
	t.Helper()
	store := newFakeR2()
	store.objects["www/deploys/"+unlockFailDeployID+"/index.html"] = []byte("hi")
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Locker = unlockErrLocker{}
	h.Index = &fakeIndex{fail: broken}
	fa := &fakeAudit{}
	h.Audit = fa
	body, _ := json.Marshal(SiteRollbackRequest{To: unlockFailDeployID})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	return w, fa
}

func runUnlockFailFinalize(t *testing.T, broken bool) (*httptest.ResponseRecorder, *fakeAudit) {
	t.Helper()
	store := newFakeR2()
	store.objects["www/deploys/"+unlockFailDeployID+"/index.html"] = []byte("hi")
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	h.Locker = unlockErrLocker{}
	h.Index = &fakeIndex{fail: broken}
	fa := &fakeAudit{}
	h.Audit = fa
	tok, _, err := jwt.Sign("alice", "www", unlockFailDeployID)
	require.NoError(t, err)
	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview", Files: []string{"index.html"}})
	w := withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+unlockFailDeployID+"/finalize", body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		context.Background(),
	)
	return w, fa
}

type unlockFailRow struct {
	name     string
	action   string
	status   int
	bodyKey  string
	bodyVal  any
	failCode string
	run      func(*testing.T, bool) (*httptest.ResponseRecorder, *fakeAudit)
}

func unlockFailRows() []unlockFailRow {
	return []unlockFailRow{
		{"site.update", "site.update", http.StatusOK, "slug", "example", "registry_write_failed", runUnlockFailUpdate},
		{"site.purge", "site.purge", http.StatusOK, "status", "purged", "r2_move_failed", runUnlockFailPurge},
		{"site.deploy.delete", "site.deploy.delete", http.StatusOK, "status", "tombstoned", "r2_move_failed", runUnlockFailDeployDelete},
		{"site.deploy.restore", "site.deploy.restore", http.StatusOK, "status", "restored", "r2_move_failed", runUnlockFailDeployRestore},
		{"site.promote", "site.promote", http.StatusOK, "deployId", unlockFailDeployID, "pg_write_failed", runUnlockFailPromote},
		{"site.rollback", "site.rollback", http.StatusOK, "deployId", unlockFailDeployID, "pg_write_failed", runUnlockFailRollback},
		{"deploy.finalize", "deploy.finalize", http.StatusOK, "deployId", unlockFailDeployID, "pg_write_failed", runUnlockFailFinalize},
	}
}

func TestUnlockFail_CommittedWorkAuditsOnceAndWritesOneBody(t *testing.T) {
	for _, row := range unlockFailRows() {
		t.Run(row.name, func(t *testing.T) {
			w, fa := row.run(t, false)

			require.Equal(t, row.status, w.Code, w.Body.String())
			body := decodeSingleBody(t, w)
			assert.Equal(t, row.bodyVal, body[row.bodyKey])
			require.Len(t, fa.events, 1,
				"the work committed; releasing the lock afterwards is not the caller's business")
			assert.Equal(t, row.action, fa.events[0].Action)
			assert.Equal(t, "success", fa.events[0].Outcome)
		})
	}
}

func TestUnlockFail_HandledFailureWritesOneBody(t *testing.T) {
	for _, row := range unlockFailRows() {
		t.Run(row.name, func(t *testing.T) {
			w, _ := row.run(t, true)

			require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
			body := decodeSingleBody(t, w)
			errObj, ok := body["error"].(map[string]any)
			require.True(t, ok, w.Body.String())
			assert.Equal(t, row.failCode, errObj["code"])
		})
	}
}
