package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeployInit_RejectsShaOutsideCharset(t *testing.T) {
	bad := []struct {
		name string
		sha  string
	}{
		{"semver dots", "v1.2.3"},
		{"underscore", "a_b"},
		{"space", "a b"},
		{"dotdot", ".."},
		{"slash", "a/b"},
		{"over 64", strings.Repeat("a", 65)},
		{"nul bytes", string(make([]byte, 8))},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newTestHandlers(t, authedGH(), standardSites(), newFakeR2())
			body, _ := json.Marshal(DeployInitRequest{Site: "www", SHA: tc.sha})
			w := withChiRoute(http.MethodPost, "/api/deploy/init", "/api/deploy/init", body,
				nil, h.DeployInit,
				contextWithLogin(context.Background(), "alice", "tok"),
			)
			require.Equal(t, http.StatusBadRequest, w.Code,
				"sha %q must be refused before an id is minted: %s", tc.sha, w.Body.String())
		})
	}

	t.Run("valid sha mints pattern-conformant id", func(t *testing.T) {
		h, _ := newTestHandlers(t, authedGH(), standardSites(), newFakeR2())
		body, _ := json.Marshal(DeployInitRequest{Site: "www", SHA: strings.Repeat("a", 64)})
		w := withChiRoute(http.MethodPost, "/api/deploy/init", "/api/deploy/init", body,
			nil, h.DeployInit,
			contextWithLogin(context.Background(), "alice", "tok"),
		)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var resp DeployInitResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Regexp(t, deployIDPattern, resp.DeployID)
	})
}
