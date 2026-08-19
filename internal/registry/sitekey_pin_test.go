package registry

import (
	"context"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

var (
	_ func(Snapshot) []sitekey.Slug                                               = Snapshot.Sites
	_ func(Snapshot, sitekey.Slug) []string                                       = Snapshot.TeamsForSite
	_ func(Writer, context.Context, sitekey.Slug) (Site, error)                   = Writer.GetSite
	_ func(Writer, context.Context, sitekey.Slug, []string, string) (Site, error) = Writer.Register
	_ func(Writer, context.Context, sitekey.Slug, []string) (Site, error)         = Writer.UpdateTeams
	_ func(Writer, context.Context, sitekey.Slug) error                           = Writer.Delete
	_ sitekey.Slug                                                                = Site{}.Slug
)
