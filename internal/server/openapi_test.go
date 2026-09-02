package server

import (
	"net/http"
	"sort"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

const openAPIPath = "../../docs/api/openapi.yaml"

func loadOpenAPI(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(openAPIPath)
	require.NoError(t, err)
	return doc
}

func TestOpenAPI_DocumentIsValid(t *testing.T) {
	require.NoError(t, loadOpenAPI(t).Validate(t.Context()))
}

func TestOpenAPI_NamesExactlyTheMountedRoutes(t *testing.T) {
	var spec []string
	for path, item := range loadOpenAPI(t).Paths.Map() {
		for method := range item.Operations() {
			spec = append(spec, method+" "+path)
		}
	}
	router := New(&handler.Handlers{Repos: stubRepoStore{}, GitHubApp: stubRepoCreator{}, RepoGH: stubRepoGH{}})
	var mounted []string
	err := chi.Walk(router.(chi.Router), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted = append(mounted, method+" "+route)
		return nil
	})
	require.NoError(t, err)
	sort.Strings(spec)
	sort.Strings(mounted)
	require.Equal(t, mounted, spec, "docs/api/openapi.yaml must name exactly the routes internal/server mounts")
}
