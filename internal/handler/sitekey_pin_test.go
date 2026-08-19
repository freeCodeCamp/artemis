package handler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	_ sitekey.Slug = SiteRow{}.Slug
	_ sitekey.Slug = SiteRegisterRequest{}.Slug
	_ sitekey.Slug = DeployInitRequest{}.Site

	_ func(*Handlers, sitekey.Slug, string) string = (*Handlers).deployPrefix
	_ func(*Handlers, sitekey.Slug, string) string = (*Handlers).aliasKey
	_ func(*Handlers, sitekey.Slug, string) string = (*Handlers).publicURL
	_ func(*Handlers, sitekey.Slug, string) string = (*Handlers).trashPrefix
)

func TestSiteRow_SlugMarshalsAsAPlainJSONString(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(SiteRow{Slug: "example", CreatedAt: time.Unix(0, 0).UTC()})
	require.NoError(t, err)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(raw, &wire))
	assert.Equal(t, "example", wire["slug"])
}

func TestSiteRegisterRequest_SlugUnmarshalsFromAPlainJSONString(t *testing.T) {
	t.Parallel()

	var req SiteRegisterRequest
	require.NoError(t, json.Unmarshal([]byte(`{"slug":"example","teams":["staff"]}`), &req))
	assert.Equal(t, sitekey.Slug("example"), req.Slug)
}
