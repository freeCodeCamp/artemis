package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/stretchr/testify/require"
)

const openAPIServer = "https://uploads.freecode.camp"

type failingHealth struct{}

func (failingHealth) Ping(context.Context) error { return errors.New("upstream down") }

func openAPIRouter(t *testing.T) routers.Router {
	t.Helper()
	router, err := legacy.NewRouter(loadOpenAPI(t))
	require.NoError(t, err)
	return router
}

func assertResponseMatchesSpec(t *testing.T, router routers.Router, r *http.Request, w *httptest.ResponseRecorder) {
	t.Helper()
	route, pathParams, err := router.FindRoute(r)
	require.NoError(t, err)

	err = openapi3filter.ValidateResponse(t.Context(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request:    r,
			PathParams: pathParams,
			Route:      route,
		},
		Status:  w.Code,
		Header:  w.Header(),
		Body:    io.NopCloser(bytes.NewReader(w.Body.Bytes())),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	})
	require.NoError(t, err, "body=%s", w.Body.String())
}

func TestOpenAPI_ReadyzBodiesMatchTheSpec(t *testing.T) {
	router := openAPIRouter(t)

	for name, h := range map[string]*handler.Handlers{
		"all upstreams answer": {},
		"valkey down":          {Health: failingHealth{}},
		"postgres down":        {PGHealth: failingHealth{}},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, openAPIServer+"/readyz", nil)
			w := httptest.NewRecorder()
			h.ReadyZ(w, r)
			assertResponseMatchesSpec(t, router, r, w)
		})
	}
}

func TestOpenAPI_HealthzBodyMatchesTheSpec(t *testing.T) {
	router := openAPIRouter(t)
	r := httptest.NewRequest(http.MethodGet, openAPIServer+"/healthz", nil)
	w := httptest.NewRecorder()
	(&handler.Handlers{}).HealthZ(w, r)
	assertResponseMatchesSpec(t, router, r, w)
}
