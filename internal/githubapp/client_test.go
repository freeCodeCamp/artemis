package githubapp

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
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

func TestClient_InstallationTokenSurfacesGitHubMessage(t *testing.T) {
	const ghMsg = "'Expiration time' claim ('exp') is too far in the future"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access_tokens") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"`+ghMsg+`"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	_, err := c.installationToken(context.Background())
	if err == nil {
		t.Fatal("expected error on 401 token mint")
	}
	if !strings.Contains(err.Error(), ghMsg) {
		t.Errorf("error %q does not surface the GitHub message %q", err.Error(), ghMsg)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q should include the status code", err.Error())
	}
}

func TestClient_ListTemplatesSurfacesGitHubMessageNoLeak(t *testing.T) {
	const ghMsg = "Resource not accessible by integration"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			tokenResponse(w)
		case strings.Contains(r.URL.Path, "/repos"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"`+ghMsg+`"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	_, err := c.ListTemplates(context.Background())
	if err == nil {
		t.Fatal("expected error on 403 list repos")
	}
	if !strings.Contains(err.Error(), ghMsg) {
		t.Errorf("error %q does not surface the GitHub message", err.Error())
	}
	if strings.Contains(err.Error(), "Bearer ") || strings.Contains(err.Error(), "ghs_") {
		t.Errorf("error %q leaks a secret", err.Error())
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

func writeGitHubJWTError(w http.ResponseWriter, msg string) {
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, fmt.Sprintf(`{"message":%q}`, msg))
}

func githubTokenHandlerValidatingJWT(
	t *testing.T,
	pub *rsa.PublicKey,
	appID string,
	now func() time.Time,
) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/access_tokens") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		claims := &jwt.RegisteredClaims{}
		tok, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected alg %v", token.Header["alg"])
			}
			return pub, nil
		}, jwt.WithoutClaimsValidation())
		if err != nil || !tok.Valid || claims.Issuer != appID {
			writeGitHubJWTError(w, "A JSON web token could not be decoded")
			return
		}
		if claims.ExpiresAt == nil || claims.ExpiresAt.Time.After(now().Add(600*time.Second)) {
			writeGitHubJWTError(w, "'Expiration time' claim ('exp') is too far in the future")
			return
		}
		tokenResponse(w)
	}
}

func TestClient_InstallationToken_AppJWTPassesGitHubValidation(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }
	key, pem := testRSAKeyPKCS1(t)
	signer, err := NewAppJWTSigner("123", pem)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	signer.now = now

	srv := httptest.NewServer(githubTokenHandlerValidatingJWT(t, &key.PublicKey, "123", now))
	defer srv.Close()

	c, err := NewClient(ClientConfig{
		APIBase:        srv.URL,
		Org:            testOrg,
		InstallationID: "42",
		Signer:         signer,
		Now:            now,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	tok, err := c.installationToken(context.Background())
	if err != nil {
		t.Fatalf("installationToken rejected by validating fake: %v", err)
	}
	if tok != "ghs_inst" {
		t.Errorf("token = %q, want ghs_inst", tok)
	}
}

func TestGitHubTokenHandler_RejectsOverCapAppJWT(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) }
	key, _ := testRSAKeyPKCS1(t)

	overCap := jwt.RegisteredClaims{
		Issuer:    "123",
		IssuedAt:  jwt.NewNumericDate(now()),
		ExpiresAt: jwt.NewNumericDate(now().Add(700 * time.Second)),
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodRS256, overCap).SignedString(key)
	if err != nil {
		t.Fatalf("sign over-cap jwt: %v", err)
	}

	srv := httptest.NewServer(githubTokenHandlerValidatingJWT(t, &key.PublicKey, "123", now))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/app/installations/42/access_tokens", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for over-cap App JWT (B6 regression guard)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "too far in the future") {
		t.Errorf("body = %q, want exp-too-far rejection", body)
	}
}
