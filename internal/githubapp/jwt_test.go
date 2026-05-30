package githubapp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func testRSAKeyPKCS1(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, string(pemBytes)
}

func testRSAKeyPKCS8(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return key, string(pemBytes)
}

func TestNewAppJWTSigner_AcceptsPKCS1AndPKCS8(t *testing.T) {
	_, pkcs1 := testRSAKeyPKCS1(t)
	if _, err := NewAppJWTSigner("123", pkcs1); err != nil {
		t.Errorf("PKCS#1 key rejected: %v", err)
	}
	_, pkcs8 := testRSAKeyPKCS8(t)
	if _, err := NewAppJWTSigner("123", pkcs8); err != nil {
		t.Errorf("PKCS#8 key rejected: %v", err)
	}
}

func TestNewAppJWTSigner_Rejects(t *testing.T) {
	_, pkcs1 := testRSAKeyPKCS1(t)
	if _, err := NewAppJWTSigner("", pkcs1); err == nil {
		t.Error("empty app id must be rejected")
	}
	if _, err := NewAppJWTSigner("123", "not a pem"); err == nil {
		t.Error("non-PEM key must be rejected")
	}
	// EC key in PKCS#8 → not RSA → reject.
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(ec)
	ecPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := NewAppJWTSigner("123", string(ecPEM)); err == nil {
		t.Error("non-RSA key must be rejected")
	}
}

func TestAppJWTSigner_Sign(t *testing.T) {
	key, pemStr := testRSAKeyPKCS1(t)
	signer, err := NewAppJWTSigner("987654", pemStr)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	fixed := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	signer.now = func() time.Time { return fixed }

	tokenStr, err := signer.Sign()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	claims := jwt.RegisteredClaims{}
	// Pin the verifier's clock to the same fixed time used to sign, so
	// exp/iat are validated deterministically. Without WithTimeFunc the
	// parser uses real wall-clock and the token (exp = fixed+540s) reads
	// as expired whenever the suite runs >10min after `fixed` (CI flake).
	parser := jwt.NewParser(jwt.WithTimeFunc(func() time.Time { return fixed }))
	parsed, err := parser.ParseWithClaims(tokenStr, &claims, func(tok *jwt.Token) (any, error) {
		if tok.Method.Alg() != "RS256" {
			t.Errorf("alg = %s, want RS256", tok.Method.Alg())
		}
		return &key.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("parse/verify: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("token not valid")
	}
	if claims.Issuer != "987654" {
		t.Errorf("iss = %q, want 987654", claims.Issuer)
	}
	if got := claims.IssuedAt.Time; !got.Equal(fixed.Add(-60 * time.Second)) {
		t.Errorf("iat = %v, want %v", got, fixed.Add(-60*time.Second))
	}
	if got := claims.ExpiresAt.Time; !got.Equal(fixed.Add(540 * time.Second)) {
		t.Errorf("exp = %v, want %v", got, fixed.Add(540*time.Second))
	}
}

func TestAppJWTSigner_ExpWithinGitHubTenMinuteCap(t *testing.T) {
	key, pemStr := testRSAKeyPKCS1(t)
	signer, err := NewAppJWTSigner("987654", pemStr)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	fixed := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	signer.now = func() time.Time { return fixed }

	tokenStr, err := signer.Sign()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	claims := jwt.RegisteredClaims{}
	parser := jwt.NewParser(jwt.WithTimeFunc(func() time.Time { return fixed }))
	if _, err := parser.ParseWithClaims(tokenStr, &claims, func(*jwt.Token) (any, error) {
		return &key.PublicKey, nil
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	iat := claims.IssuedAt.Time
	exp := claims.ExpiresAt.Time
	if d := exp.Sub(fixed); d > 600*time.Second {
		t.Errorf("exp is %v ahead of now, exceeds GitHub's 600s cap", d)
	}
	if d := exp.Sub(fixed); d != 540*time.Second {
		t.Errorf("exp-now = %v, want 540s (60s margin under the 600s cap)", d)
	}
	if d := exp.Sub(iat); d != 600*time.Second {
		t.Errorf("exp-iat = %v, want 600s", d)
	}
}
