// Package githubapp mints Apollo-11 GitHub App credentials server-side
// and drives the repo-creation REST calls. The App private key is a
// cluster secret (same class as the R2 keys) and never leaves this
// process — the universe-cli only ever sends a user bearer; the App
// JWT → installation-token → repo-create chain is fully server-internal.
package githubapp

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// appJWTClockSkew back-dates iat to tolerate clock drift between artemis
// and GitHub. appJWTTTL is the lifetime; GitHub caps App JWTs at 10 min.
const (
	appJWTClockSkew = 60 * time.Second
	appJWTTTL       = 600 * time.Second
)

// AppJWTSigner mints short-lived RS256 App JWTs whose `iss` is the App
// id. The signed JWT is exchanged for an installation access token by
// Client.
type AppJWTSigner struct {
	appID string
	key   *rsa.PrivateKey
	now   func() time.Time
}

// NewAppJWTSigner parses the PEM-encoded RSA private key (PKCS#1 — what
// GitHub issues — or PKCS#8) and returns a ready signer.
func NewAppJWTSigner(appID, privateKeyPEM string) (*AppJWTSigner, error) {
	if appID == "" {
		return nil, errors.New("githubapp: empty app id")
	}
	key, err := parseRSAPrivateKey([]byte(privateKeyPEM))
	if err != nil {
		return nil, err
	}
	return &AppJWTSigner{appID: appID, key: key, now: time.Now}, nil
}

// Sign returns a freshly minted App JWT. iat is back-dated by
// appJWTClockSkew, exp is iat+appJWTTTL, keeping exp at now+540s — under
// GitHub's now+600s cap so a small positive clock skew cannot trip the
// "'exp' is too far in the future" rejection.
func (s *AppJWTSigner) Sign() (string, error) {
	now := s.now()
	iat := now.Add(-appJWTClockSkew)
	claims := jwt.RegisteredClaims{
		Issuer:    s.appID,
		IssuedAt:  jwt.NewNumericDate(iat),
		ExpiresAt: jwt.NewNumericDate(iat.Add(appJWTTTL)),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(s.key)
	if err != nil {
		return "", fmt.Errorf("githubapp: sign app jwt: %w", err)
	}
	return signed, nil
}

// parseRSAPrivateKey accepts a single PEM block holding an RSA private
// key in PKCS#1 ("BEGIN RSA PRIVATE KEY") or PKCS#8 ("BEGIN PRIVATE
// KEY") form. GitHub issues PKCS#1; PKCS#8 is accepted for keys that
// have been converted (e.g. via openssl).
func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("githubapp: no PEM block in private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("githubapp: parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("githubapp: private key is not RSA")
	}
	return key, nil
}
