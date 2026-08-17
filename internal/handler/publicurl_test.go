package handler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublicURL_RendersFromTheConfiguredFormats(t *testing.T) {
	t.Parallel()

	h := &Handlers{
		PublicProductionURLFmt: "https://<site>.example.test",
		PublicPreviewURLFmt:    "https://<site>.pre.example.test",
	}

	require.Equal(t, "https://www.example.test", h.publicURL("www", "production"))
	require.Equal(t, "https://www.pre.example.test", h.publicURL("www", "preview"),
		"the URL handed to the CLI must come from config, not a constant baked into the binary")
}
