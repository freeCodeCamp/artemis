package handler

import (
	"net/http"
	"sort"
)

// WhoAmI implements GET /api/whoami. Returns the resolved login plus the
// list of sites the caller is authorized to deploy to. The list is
// computed by intersecting sites.yaml with the caller's team memberships.
func (h *Handlers) WhoAmI(w http.ResponseWriter, r *http.Request) {
	login := LoginFromContext(r.Context())
	token := GitHubTokenFromContext(r.Context())

	authorized := []string{}
	snap := h.Sites.Snapshot()
	for _, site := range snap.Sites() {
		teams := snap.TeamsForSite(site)
		if len(teams) == 0 {
			continue
		}
		ok, err := h.GH.AuthorizeForSite(r.Context(), token, login, teams)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", "could not probe team membership")
			return
		}
		if ok {
			authorized = append(authorized, site)
		}
	}
	sort.Strings(authorized)
	writeJSON(w, http.StatusOK, map[string]any{
		"login":           login,
		"authorizedSites": authorized,
	})
}
