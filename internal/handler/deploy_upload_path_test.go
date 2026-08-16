package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeployUpload_AbsolutePathRefused(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"absolute", "/index.html"},
		{"double slash", "//index.html"},
		{"bare slash", "/"},
		{"absolute nested", "/assets/app.js"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeR2()
			h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
			deployID := "20260420-141522-abc1234"
			tok, _, err := jwt.Sign("alice", "www", deployID)
			require.NoError(t, err)

			w := withChiRoute(http.MethodPut, "/api/deploy/{deployId}/upload",
				"/api/deploy/"+deployID+"/upload?path="+tc.path,
				[]byte("<h1>hi</h1>"),
				map[string]string{"Authorization": "Bearer " + tok},
				h.RequireDeployJWT(http.HandlerFunc(h.DeployUpload)).ServeHTTP,
				context.Background(),
			)
			require.Equal(t, http.StatusBadRequest, w.Code,
				"an absolute ?path= must be refused, not silently rewritten: %s", w.Body.String())
			require.Empty(t, store.objects, "nothing may reach R2 on a refused path")
		})
	}
}
