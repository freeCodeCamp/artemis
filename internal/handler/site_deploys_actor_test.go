package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSiteDeploys_AttachesActorFromAudit(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.objects["www/deploys/d1/index.html"] = []byte("a")
	store.objects["www/deploys/d2/index.html"] = []byte("c")
	h, _ := newTestHandlers(t, gh, standardSites(), store)
	h.Audit = &fakeAudit{deployActors: map[string]string{"d1": "alice"}}

	w := withSiteRoute(http.MethodGet, "/api/site/{site}/deploys",
		"/api/site/www/deploys", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteDeploys,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	byID := map[string]map[string]any{}
	for _, row := range rows {
		byID[row["deployId"].(string)] = row
	}
	assert.Equal(t, "alice", byID["d1"]["actor"], "the finalizing actor is joined onto the deploy row")
	_, hasActor := byID["d2"]["actor"]
	assert.False(t, hasActor, "a deploy with no finalize audit row carries no actor")
}

func TestSiteDeploys_ActorJoinFailSoft(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.objects["www/deploys/d1/index.html"] = []byte("a")
	h, _ := newTestHandlers(t, gh, standardSites(), store)
	h.Audit = &fakeAudit{deployActorsErr: errors.New("pg down")}

	w := withSiteRoute(http.MethodGet, "/api/site/{site}/deploys",
		"/api/site/www/deploys", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteDeploys,
	)
	require.Equal(t, http.StatusOK, w.Code, "an audit read failure must not break the deploy list")
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
}
