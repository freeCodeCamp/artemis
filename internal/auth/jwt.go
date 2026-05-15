// Package auth implements the two trust planes used by Artemis:
//
//   - Deploy-session JWTs (HS256, ≤15 min, scoped to (login, site, deployId))
//     issued by /api/deploy/init and consumed by /upload + /finalize.
//   - GitHub identity + team-membership probe (in github.go).
//
// The deploy-session JWT is the only JWT artemis mints today; a separate
// auth-session JWT was considered and parked — the narrow per-deploy
// scope is enough for the upload/finalize handoff without a longer-lived
// session token.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	jwtIssuer       = "artemis"
	minJWTKeyLength = 32
)

// DeploySessionClaims are the custom claims carried in a deploy-session
// JWT. The narrow scope is (login, site, deployId): any of the three
// missing or mismatched on Verify is a hard reject.
//
// `sub` (login) and `iss` come from the embedded RegisteredClaims so
// there is exactly one source of each on the wire. An earlier shape
// had outer Login/Issuer fields that shadowed the embedded ones at
// marshal time and silently dropped the embedded values on the wire.
type DeploySessionClaims struct {
	Site     string `json:"site"`
	DeployID string `json:"deployId"`
	jwt.RegisteredClaims
}

// RequireScope verifies that the JWT was issued for exactly this
// (login, site, deployId) triple. Returns an error otherwise.
func (c DeploySessionClaims) RequireScope(login, site, deployID string) error {
	if c.Subject != login {
		return fmt.Errorf("auth: jwt sub %q != expected %q", c.Subject, login)
	}
	if c.Site != site {
		return fmt.Errorf("auth: jwt site %q != expected %q", c.Site, site)
	}
	if c.DeployID != deployID {
		return fmt.Errorf("auth: jwt deployId %q != expected %q", c.DeployID, deployID)
	}
	return nil
}

// DeploySessionSigner mints + verifies HS256 deploy-session JWTs.
type DeploySessionSigner struct {
	key []byte
	ttl time.Duration
}

// NewDeploySessionSigner returns a signer with the given HS256 secret and
// TTL. The secret must be at least 32 bytes and the TTL must be positive.
func NewDeploySessionSigner(secret string, ttl time.Duration) (*DeploySessionSigner, error) {
	if len(secret) < minJWTKeyLength {
		return nil, fmt.Errorf("auth: signing key must be at least %d bytes (got %d)", minJWTKeyLength, len(secret))
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("auth: ttl must be positive (got %v)", ttl)
	}
	return &DeploySessionSigner{
		key: []byte(secret),
		ttl: ttl,
	}, nil
}

// Sign issues a JWT scoped to (login, site, deployId). Returns the token
// string and its absolute expiry time.
func (s *DeploySessionSigner) Sign(login, site, deployID string) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(s.ttl)
	claims := DeploySessionClaims{
		Site:     site,
		DeployID: deployID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   login,
			Issuer:    jwtIssuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign: %w", err)
	}
	return signed, exp, nil
}

// Verify parses and validates the token. Returns the claims on success.
// On failure callers can use IsExpired(err) to distinguish expired-token
// (401) from other failures.
func (s *DeploySessionSigner) Verify(token string) (DeploySessionClaims, error) {
	var claims DeploySessionClaims
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"HS256"}))
	_, err := parser.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method %v", t.Header["alg"])
		}
		return s.key, nil
	})
	if err != nil {
		return DeploySessionClaims{}, err
	}
	if claims.Issuer != jwtIssuer {
		return DeploySessionClaims{}, fmt.Errorf("auth: jwt issuer %q != expected %q", claims.Issuer, jwtIssuer)
	}
	return claims, nil
}

// IsExpired reports whether err was produced by a JWT whose ExpiresAt has
// passed. Used by handlers to map expired → 401 vs other validation
// failures → 403.
func IsExpired(err error) bool {
	return errors.Is(err, jwt.ErrTokenExpired)
}
