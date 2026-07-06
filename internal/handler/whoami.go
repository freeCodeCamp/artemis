package handler

import (
	"net/http"
	"sort"
)

// WhoAmI implements GET /api/whoami. Returns the resolved login plus the
// list of sites the caller is authorized to deploy to.
//
// Pre-B9 this looped registered sites × per-site teams and made one
// AuthorizeForSite (→ N IsTeamMember) call per site. With S sites and
// T teams each, cold-cache cost was up to S×T sequential GitHub round
// trips, all serialized on a single mutex.
//
// Post-B9: a single GET /user/teams returns every team the token is
// on; intersection with the registry snapshot is local. Authorization
// for individual deploys still uses AuthorizeForSite — that's a
// per-deploy hot path with its own team cache.
func (h *Handlers) WhoAmI(w http.ResponseWriter, r *http.Request) {
	login := LoginFromContext(r.Context())
	token := GitHubTokenFromContext(r.Context())

	teams, err := h.GH.UserTeams(r.Context(), token)
	if err != nil {
		writeGitHubProbeError(w, err)
		return
	}
	userTeams := make(map[string]struct{}, len(teams))
	for _, t := range teams {
		userTeams[t] = struct{}{}
	}

	authorized := []string{}
	snap := h.Sites.Snapshot()
	for _, site := range snap.Sites() {
		siteTeams := snap.TeamsForSite(site)
		if len(siteTeams) == 0 {
			continue
		}
		for _, st := range siteTeams {
			if _, ok := userTeams[st]; ok {
				authorized = append(authorized, site)
				break
			}
		}
	}
	sort.Strings(authorized)
	writeJSON(w, http.StatusOK, map[string]any{
		"login":           login,
		"authorizedSites": authorized,
	})
}
