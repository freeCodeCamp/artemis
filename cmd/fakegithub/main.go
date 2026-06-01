package main

import (
	"crypto/rsa"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

type config struct {
	addr      string
	org       string
	user      string
	teams     map[string]bool
	teamList  []string
	appID     string
	pubKey    *rsa.PublicKey
	templates []string
}

type repo struct {
	Name       string `json:"name"`
	FullName   string `json:"full_name"`
	HTMLURL    string `json:"html_url"`
	Private    bool   `json:"private"`
	IsTemplate bool   `json:"is_template"`
}

type server struct {
	cfg   config
	mu    sync.Mutex
	repos map[string]*repo
}

func main() {
	cfg := loadConfig()
	srv := &server{cfg: cfg, repos: map[string]*repo{}}
	for _, name := range cfg.templates {
		srv.repos[name] = &repo{
			Name:       name,
			FullName:   cfg.org + "/" + name,
			HTMLURL:    "http://fakegithub/" + cfg.org + "/" + name,
			IsTemplate: true,
		}
	}

	r := chi.NewRouter()
	r.Get("/user", srv.handleUser)
	r.Get("/user/teams", srv.handleUserTeams)
	r.Get("/orgs/{org}/teams/{team}/memberships/{login}", srv.handleMembership)
	r.Post("/app/installations/{id}/access_tokens", srv.handleAccessToken)
	r.Get("/orgs/{org}/repos", srv.handleListRepos)
	r.Post("/orgs/{org}/repos", srv.handleCreateRepo)
	r.Post("/repos/{org}/{template}/generate", srv.handleGenerate)
	r.Get("/repos/{org}/{name}", srv.handleGetRepo)
	r.Put("/repos/{org}/{name}/actions/permissions", srv.handlePermissions)
	r.Get("/repos/{org}/{name}/contents/*", srv.handleContents)

	slog.Info("fakegithub listening", "addr", cfg.addr, "org", cfg.org, "user", cfg.user,
		"teams", cfg.teamList, "appID", cfg.appID, "jwtVerify", cfg.pubKey != nil)
	if err := http.ListenAndServe(cfg.addr, r); err != nil {
		slog.Error("fakegithub exited", "err", err)
		os.Exit(1)
	}
}

func loadConfig() config {
	cfg := config{
		addr:  envOr("FAKE_GH_ADDR", ":9000"),
		org:   envOr("FAKE_GH_ORG", "freeCodeCamp-Universe"),
		user:  envOr("FAKE_GH_USER", "smoke-bot"),
		appID: envOr("FAKE_GH_APP_ID", "123"),
	}
	cfg.teamList = splitCSV(envOr("FAKE_GH_TEAMS", "staff,apollo-11-approvers"))
	cfg.teams = map[string]bool{}
	for _, t := range cfg.teamList {
		cfg.teams[t] = true
	}
	cfg.templates = splitCSV(envOr("FAKE_GH_TEMPLATES", "universe-static-template"))
	if pem := os.Getenv("FAKE_GH_APP_PUBLIC_KEY"); pem != "" {
		key, err := jwt.ParseRSAPublicKeyFromPEM([]byte(pem))
		if err != nil {
			slog.Error("FAKE_GH_APP_PUBLIC_KEY parse failed", "err", err)
			os.Exit(2)
		}
		cfg.pubKey = key
	}
	return cfg
}

func (s *server) handleUser(w http.ResponseWriter, r *http.Request) {
	if bearer(r) == "" {
		writeGH(w, http.StatusUnauthorized, map[string]string{"message": "Requires authentication"})
		return
	}
	writeGH(w, http.StatusOK, map[string]any{"login": s.cfg.user, "id": 1})
}

func (s *server) handleUserTeams(w http.ResponseWriter, r *http.Request) {
	if bearer(r) == "" {
		writeGH(w, http.StatusUnauthorized, map[string]string{"message": "Requires authentication"})
		return
	}
	out := make([]map[string]any, 0, len(s.cfg.teamList))
	for _, t := range s.cfg.teamList {
		out = append(out, map[string]any{
			"slug":         t,
			"organization": map[string]any{"login": s.cfg.org},
		})
	}
	writeGH(w, http.StatusOK, out)
}

func (s *server) handleMembership(w http.ResponseWriter, r *http.Request) {
	team := chi.URLParam(r, "team")
	login := chi.URLParam(r, "login")
	if login == s.cfg.user && s.cfg.teams[team] {
		writeGH(w, http.StatusOK, map[string]any{"state": "active", "role": "member"})
		return
	}
	writeGH(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

func (s *server) handleAccessToken(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if raw == "" {
		writeGH(w, http.StatusUnauthorized, map[string]string{"message": "A JSON web token could not be decoded"})
		return
	}
	if s.cfg.pubKey != nil {
		claims := &jwt.RegisteredClaims{}
		tok, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, jwt.ErrTokenSignatureInvalid
			}
			return s.cfg.pubKey, nil
		})
		if err != nil || !tok.Valid || claims.Issuer != s.cfg.appID {
			writeGH(w, http.StatusUnauthorized, map[string]string{"message": "A JSON web token could not be decoded"})
			return
		}
		if claims.ExpiresAt == nil || claims.ExpiresAt.Time.After(time.Now().Add(600*time.Second)) {
			writeGH(w, http.StatusUnauthorized, map[string]string{"message": "'Expiration time' claim ('exp') is too far in the future"})
			return
		}
	}
	writeGH(w, http.StatusCreated, map[string]any{
		"token":      "ghs_fakeinstallationtoken",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
}

func (s *server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	out := make([]*repo, 0, len(s.repos))
	for _, rp := range s.repos {
		out = append(out, rp)
	}
	s.mu.Unlock()
	writeGH(w, http.StatusOK, out)
}

func (s *server) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Private     bool   `json:"private"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeGH(w, http.StatusUnprocessableEntity, map[string]string{"message": "Invalid request"})
		return
	}
	s.create(w, body.Name, body.Private)
}

func (s *server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	template := chi.URLParam(r, "template")
	s.mu.Lock()
	_, known := s.repos[template]
	s.mu.Unlock()
	if !known {
		writeGH(w, http.StatusUnprocessableEntity, map[string]string{"message": "Could not clone: template not accessible"})
		return
	}
	var body struct {
		Name    string `json:"name"`
		Private bool   `json:"private"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeGH(w, http.StatusUnprocessableEntity, map[string]string{"message": "Invalid request"})
		return
	}
	s.create(w, body.Name, body.Private)
}

func (s *server) create(w http.ResponseWriter, name string, private bool) {
	s.mu.Lock()
	if _, exists := s.repos[name]; exists {
		s.mu.Unlock()
		writeGH(w, http.StatusUnprocessableEntity, map[string]string{"message": "name already exists on this account"})
		return
	}
	rp := &repo{
		Name:     name,
		FullName: s.cfg.org + "/" + name,
		HTMLURL:  "http://fakegithub/" + s.cfg.org + "/" + name,
		Private:  private,
	}
	s.repos[name] = rp
	s.mu.Unlock()
	writeGH(w, http.StatusCreated, rp)
}

func (s *server) handleGetRepo(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	s.mu.Lock()
	rp, ok := s.repos[name]
	s.mu.Unlock()
	if !ok {
		writeGH(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeGH(w, http.StatusOK, rp)
}

func (s *server) handlePermissions(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleContents(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	s.mu.Lock()
	_, ok := s.repos[name]
	s.mu.Unlock()
	if !ok {
		writeGH(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeGH(w, http.StatusOK, []any{})
}

func writeGH(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
