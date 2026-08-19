package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	_ func(*DeploySessionSigner, string, sitekey.Slug, string) (string, time.Time, error) = (*DeploySessionSigner).Sign
	_ sitekey.Slug                                                                        = DeploySessionClaims{}.Site
)

func TestSign_SiteWireEncodingIsPlainString(t *testing.T) {
	s := newSigner(t)

	tok, _, err := s.Sign("alice", sitekey.Slug("www"), "d-1")
	require.NoError(t, err)

	parts := strings.Split(tok, ".")
	require.Len(t, parts, 3)
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	assert.Equal(t, "www", payload["site"])
}
