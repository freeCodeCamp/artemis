package githubapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testOrg = "freeCodeCamp-Universe"

func newSigner(t *testing.T) *AppJWTSigner {
	t.Helper()
	_, pem := testRSAKeyPKCS1(t)
	s, err := NewAppJWTSigner("123", pem)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return s
}

func newClient(t *testing.T, base string) *Client {
	t.Helper()
	c, err := NewClient(ClientConfig{
		APIBase:        base,
		Org:            testOrg,
		InstallationID: "42",
		Signer:         newSigner(t),
		Now:            func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

func tokenResponse(w http.ResponseWriter) {
	w.WriteHeader(http.StatusCreated)
	_, _ = io.WriteString(w, `{"token":"ghs_inst","expires_at":"2026-05-29T13:00:00Z"}`)
}

func TestClient_InstallationTokenCached(t *testing.T) {
	var tokenCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access_tokens") {
			atomic.AddInt32(&tokenCalls, 1)
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Errorf("token request Authorization = %q, want Bearer <jwt>", r.Header.Get("Authorization"))
			}
			tokenResponse(w)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	tok, err := c.installationToken(context.Background())
	if err != nil {
		t.Fatalf("installationToken: %v", err)
	}
	if tok != "ghs_inst" {
		t.Errorf("token = %q, want ghs_inst", tok)
	}
	if _, err := c.installationToken(context.Background()); err != nil {
		t.Fatalf("second installationToken: %v", err)
	}
	if got := atomic.LoadInt32(&tokenCalls); got != 1 {
		t.Errorf("token minted %d times, want 1 (cached)", got)
	}
}

func TestClient_CreateBlankPrivateDisablesActions(t *testing.T) {
	var disabled int32
	var createBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access_tokens"):
			tokenResponse(w)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/"+testOrg+"/my-repo":
			w.WriteHeader(http.StatusNotFound) // does not exist yet
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/"+testOrg+"/repos":
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"full_name":"`+testOrg+`/my-repo","html_url":"https://github.com/`+testOrg+`/my-repo","private":true}`)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/actions/permissions"):
			atomic.AddInt32(&disabled, 1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	got, err := c.CreateRepo(context.Background(), CreateSpec{Name: "my-repo", Private: true, Description: "x"})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if got.FullName != testOrg+"/my-repo" || got.Visibility != "private" {
		t.Errorf("got %+v", got)
	}
	if atomic.LoadInt32(&disabled) != 1 {
		t.Error("Actions must be disabled on a private repo")
	}
	if createBody["auto_init"] != true || createBody["private"] != true {
		t.Errorf("blank-repo body wrong: %+v", createBody)
	}
}

func TestClient_CreateFromTemplate(t *testing.T) {
	var disabled int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			tokenResponse(w)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/"+testOrg+"/new-app":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/"+testOrg+"/hello-universe/generate":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"full_name":"`+testOrg+`/new-app","html_url":"https://github.com/`+testOrg+`/new-app","private":false}`)
		case strings.HasSuffix(r.URL.Path, "/actions/permissions"):
			atomic.AddInt32(&disabled, 1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	got, err := c.CreateRepo(context.Background(), CreateSpec{Name: "new-app", Template: "hello-universe", Private: false})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if got.Visibility != "public" {
		t.Errorf("visibility = %q, want public", got.Visibility)
	}
	if atomic.LoadInt32(&disabled) != 0 {
		t.Error("public repo must not disable Actions")
	}
}

func TestClient_CreateAlreadyExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			tokenResponse(w)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/"+testOrg+"/dup":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"full_name":"`+testOrg+`/dup"}`)
		default:
			t.Errorf("must not attempt creation; got %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	_, err := c.CreateRepo(context.Background(), CreateSpec{Name: "dup"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want 'already exists'", err)
	}
}

func TestClient_TemplateCloneForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			tokenResponse(w)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/"+testOrg+"/x":
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/generate"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"message":"Could not clone: repository not found or insufficient permission"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	_, err := c.CreateRepo(context.Background(), CreateSpec{Name: "x", Template: "locked"})
	if err == nil || !strings.Contains(err.Error(), "Contents:read") {
		t.Fatalf("err = %v, want Contents:read hint", err)
	}
}

func TestClient_ListTemplatesCachesWithinTTL(t *testing.T) {
	var listCalls, probeCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			tokenResponse(w)
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/"+testOrg+"/repos":
			atomic.AddInt32(&listCalls, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `[{"name":"alpha","is_template":true}]`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/"+testOrg+"/alpha/contents/":
			atomic.AddInt32(&probeCalls, 1)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	c, err := NewClient(ClientConfig{
		APIBase: srv.URL, Org: testOrg, InstallationID: "42", Signer: newSigner(t),
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	for i := 0; i < 3; i++ {
		got, err := c.ListTemplates(context.Background())
		if err != nil {
			t.Fatalf("ListTemplates #%d: %v", i, err)
		}
		if len(got) != 1 || got[0] != "alpha" {
			t.Fatalf("templates = %v, want [alpha]", got)
		}
	}
	if listCalls != 1 {
		t.Errorf("org repos list calls = %d, want 1 (cached within TTL)", listCalls)
	}
	if probeCalls != 1 {
		t.Errorf("contents probe calls = %d, want 1 (cached within TTL)", probeCalls)
	}

	// Past the TTL the cache refreshes.
	now = now.Add(templatesCacheTTL + time.Second)
	if _, err := c.ListTemplates(context.Background()); err != nil {
		t.Fatalf("ListTemplates after TTL: %v", err)
	}
	if listCalls != 2 {
		t.Errorf("org repos list calls = %d, want 2 after TTL expiry", listCalls)
	}
}

func TestClient_ListTemplatesFiltersAccessible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			tokenResponse(w)
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/"+testOrg+"/repos":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `[
				{"name":"gamma","is_template":true},
				{"name":"beta","is_template":false},
				{"name":"alpha","is_template":true}
			]`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/"+testOrg+"/alpha/contents/":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/"+testOrg+"/gamma/contents/":
			w.WriteHeader(http.StatusNotFound) // not accessible → filtered out
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	got, err := c.ListTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(got) != 1 || got[0] != "alpha" {
		t.Errorf("templates = %v, want [alpha] (gamma inaccessible, beta not template)", got)
	}
}
