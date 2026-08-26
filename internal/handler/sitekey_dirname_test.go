package handler

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const prodShapedFormat = "<site>.freecode.camp/deploys/<ts>-<sha>/"

func TestSiteDeployDelete_DirnameKeyedTombstone(t *testing.T) {
	deployID := "20260101-000000-old0001"
	store := newFakeR2()
	store.objects["www.freecode.camp/deploys/"+deployID+"/index.html"] = []byte("old")

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	tomb := &fakeTombstones{}
	h.Tombstones = tomb

	w := callDeployDelete(h, "www", deployID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	defer store.mu.Unlock()
	_, live := store.objects["www.freecode.camp/deploys/"+deployID+"/index.html"]
	assert.False(t, live, "deploy bytes must leave the live prefix")
	_, trashed := store.objects["_trash/www.freecode.camp/"+deployID+"/index.html"]
	assert.True(t, trashed, "deploy bytes must land under the dirname-keyed trash prefix")
	assert.Equal(t, []string{"www.freecode.camp/" + deployID}, tomb.recorded)
}
