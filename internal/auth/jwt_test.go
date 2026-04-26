package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testKey = "0123456789abcdef0123456789abcdef" // 32 bytes

func newSigner(t *testing.T) *DeploySessionSigner {
	t.Helper()
	s, err := NewDeploySessionSigner(testKey, 15*time.Minute)
	require.NoError(t, err)
	return s
}

func TestSignAndVerify_Roundtrip(t *testing.T) {
	s := newSigner(t)

	tok, exp, err := s.Sign("alice", "www", "20260420-141522-abc1234")
	require.NoError(t, err)
	require.NotEmpty(t, tok)
	require.WithinDuration(t, time.Now().Add(15*time.Minute), exp, 5*time.Second)

	claims, err := s.Verify(tok)
	require.NoError(t, err)
	assert.Equal(t, "alice", claims.Login)
	assert.Equal(t, "www", claims.Site)
	assert.Equal(t, "20260420-141522-abc1234", claims.DeployID)
	assert.Equal(t, "artemis", claims.Issuer)
}

func TestVerify_RejectsExpired(t *testing.T) {
	s, err := NewDeploySessionSigner(testKey, time.Millisecond)
	require.NoError(t, err)

	tok, _, err := s.Sign("alice", "www", "d-1")
	require.NoError(t, err)

	time.Sleep(20 * time.Millisecond)

	_, err = s.Verify(tok)
	require.Error(t, err)
	assert.True(t, IsExpired(err), "expected IsExpired(err) to be true; got %v", err)
}

func TestVerify_RejectsTamperedSignature(t *testing.T) {
	s := newSigner(t)
	tok, _, err := s.Sign("alice", "www", "d-1")
	require.NoError(t, err)

	// Tamper the payload (claims) segment — flipping a character there
	// always changes the signed message and therefore the expected signature.
	// (Flipping the last char of the signature itself is a no-op for HS256
	// because the trailing base64url char only contributes 4 significant bits.)
	parts := strings.Split(tok, ".")
	require.Len(t, parts, 3)
	payload := parts[1]
	require.NotEmpty(t, payload)
	flipped := flip(payload[0]) + payload[1:]
	tampered := strings.Join([]string{parts[0], flipped, parts[2]}, ".")

	_, err = s.Verify(tampered)
	require.Error(t, err)
	assert.False(t, IsExpired(err))
}

func TestVerify_RejectsWrongKey(t *testing.T) {
	s := newSigner(t)
	tok, _, err := s.Sign("alice", "www", "d-1")
	require.NoError(t, err)

	other, err := NewDeploySessionSigner("ffffffffffffffffffffffffffffffff", 15*time.Minute)
	require.NoError(t, err)

	_, err = other.Verify(tok)
	require.Error(t, err)
}

func TestRequireScope_RejectsWrongDeployID(t *testing.T) {
	s := newSigner(t)
	tok, _, err := s.Sign("alice", "www", "d-1")
	require.NoError(t, err)

	claims, err := s.Verify(tok)
	require.NoError(t, err)

	require.NoError(t, claims.RequireScope("alice", "www", "d-1"))
	require.Error(t, claims.RequireScope("alice", "www", "d-2"))
	require.Error(t, claims.RequireScope("alice", "learn", "d-1"))
	require.Error(t, claims.RequireScope("bob", "www", "d-1"))
}

func TestNewSigner_RejectsShortKey(t *testing.T) {
	_, err := NewDeploySessionSigner("tooshort", time.Minute)
	require.Error(t, err)
}

func TestNewSigner_RejectsZeroTTL(t *testing.T) {
	_, err := NewDeploySessionSigner(testKey, 0)
	require.Error(t, err)
}

// flip returns a different ASCII character than b — used to corrupt a JWT
// signature in a way that survives base64-url decoding.
func flip(b byte) string {
	if b == 'A' {
		return "B"
	}
	return "A"
}
