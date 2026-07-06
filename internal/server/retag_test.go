package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetagTransaction_RenamesToRoutePattern(t *testing.T) {
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:              "https://public@example.test/1",
		EnableTracing:    true,
		TracesSampleRate: 1.0,
	})
	require.NoError(t, err)
	hub := sentry.NewHub(client, sentry.NewScope())

	router := chi.NewRouter()
	router.Use(retagTransaction)
	router.Post("/api/site/{site}/promote", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	ctx := sentry.SetHubOnContext(context.Background(), hub)
	tx := sentry.StartTransaction(ctx, "POST /api/site/www/promote")
	req := httptest.NewRequestWithContext(tx.Context(), http.MethodPost, "/api/site/www/promote", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "POST /api/site/{site}/promote", tx.Name, "tx name retagged to method + chi pattern")
	assert.Equal(t, sentry.SourceRoute, tx.Source)

	tx.Finish()
}

func TestRetagTransaction_NoTransactionNoPanic(t *testing.T) {
	router := chi.NewRouter()
	router.Use(retagTransaction)
	router.Get("/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	require.NotPanics(t, func() { router.ServeHTTP(w, req) })
	require.Equal(t, http.StatusOK, w.Code)
}
