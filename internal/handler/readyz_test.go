package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHealth lets each readyz test drive RegistryHealth.Ping behaviour.
type fakeHealth struct {
	err error
}

func (f *fakeHealth) Ping(_ context.Context) error { return f.err }

func TestReadyZ_NoAuthRequired_BothUpstreamsReachable_ReturnsOK(t *testing.T) {
	h := &Handlers{
		Health: &fakeHealth{},
		R2:     newFakeR2(),
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"ready":true}`, w.Body.String())
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
}

func TestReadyZ_ValkeyDown_Returns503_ValkeyUnreachable(t *testing.T) {
	h := &Handlers{
		Health: &fakeHealth{err: errors.New("dial tcp valkey:6379: i/o timeout")},
		R2:     newFakeR2(),
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"valkey_unreachable"`)
}

func TestReadyZ_R2Down_Returns503_R2Unreachable(t *testing.T) {
	r2 := newFakeR2()
	r2.listErr = errors.New("s3: bucket not found")
	h := &Handlers{
		Health: &fakeHealth{},
		R2:     r2,
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"r2_unreachable"`)
}

func TestReadyZ_ValkeyFailureTakesPrecedenceOnDoubleFailure(t *testing.T) {
	r2 := newFakeR2()
	r2.listErr = errors.New("r2 also down")
	h := &Handlers{
		Health: &fakeHealth{err: errors.New("valkey down")},
		R2:     r2,
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"valkey_unreachable"`)
}
