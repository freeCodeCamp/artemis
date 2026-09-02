package handler

import (
	"fmt"
	"strings"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

// DeployPrefixTemplate is the parsed form of `DEPLOY_PREFIX_FORMAT`.
//
// Two render points are exposed: SitePrefix (everything up to but not
// including the deploy id, used by SiteDeploys to list every deploy of
// a site) and DeployPrefix (the full per-deploy prefix used by the
// upload + finalize paths).
//
// The raw format must contain `<site>` (substituted at render time per
// site) and `<ts>-<sha>` (substituted with the per-deploy id). The
// `<site>` token must appear before `<ts>-<sha>` so SitePrefix can be
// derived as the static head of the template.
type DeployPrefixTemplate struct {
	// head is the format substring before "<ts>-<sha>". Includes the
	// "<site>" token literal; site is substituted at render time.
	head string
	// tail is the format substring after "<ts>-<sha>". Trailing slash
	// is appended by the renderer if missing.
	tail string
}

const deployIDToken = "<ts>-<sha>"

const siteToken = "<site>"

// NewDeployPrefixTemplate parses raw into the rendered template.
// Returns an error if either required token is absent or if `<site>`
// appears after `<ts>-<sha>` (would leave SitePrefix unrenderable).
func NewDeployPrefixTemplate(raw string) (DeployPrefixTemplate, error) {
	idx := strings.Index(raw, deployIDToken)
	if idx < 0 {
		return DeployPrefixTemplate{}, fmt.Errorf("DEPLOY_PREFIX_FORMAT %q must contain %s", raw, deployIDToken)
	}
	head := raw[:idx]
	tail := raw[idx+len(deployIDToken):]
	if !strings.Contains(head, "<site>") {
		return DeployPrefixTemplate{}, fmt.Errorf("DEPLOY_PREFIX_FORMAT %q must contain <site> before %s", raw, deployIDToken)
	}
	return DeployPrefixTemplate{head: head, tail: tail}, nil
}

// SitePrefix returns the R2 key prefix that contains every deploy for a
// site, e.g. "www/deploys/". Always ends with "/".
func (t DeployPrefixTemplate) SitePrefix(site sitekey.Slug) string {
	p := strings.ReplaceAll(t.head, "<site>", string(site))
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

func (t DeployPrefixTemplate) SiteDirname(site sitekey.Slug) sitekey.Dirname {
	p := t.SitePrefix(site)
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return sitekey.Dirname(p[:i])
	}
	return sitekey.Dirname(p)
}

// SiteSlug inverts SiteDirname: it recovers the registry slug from the
// storage dirname that this template renders for it. ok is false when
// dirname carries neither the prefix nor the suffix the site segment
// adds, i.e. nothing this template could have produced.
func (t DeployPrefixTemplate) SiteSlug(dirname sitekey.Dirname) (sitekey.Slug, bool) {
	pattern := string(t.SiteDirname(siteToken))
	i := strings.Index(pattern, siteToken)
	if i < 0 {
		return "", false
	}
	prefix, suffix := pattern[:i], pattern[i+len(siteToken):]
	raw := string(dirname)
	if len(raw) <= len(prefix)+len(suffix) {
		return "", false
	}
	if !strings.HasPrefix(raw, prefix) || !strings.HasSuffix(raw, suffix) {
		return "", false
	}
	return sitekey.Slug(raw[len(prefix) : len(raw)-len(suffix)]), true
}

// DeployPrefix returns the R2 key prefix for one deploy, e.g.
// "www/deploys/20260420-141522-abc1234/". Always ends with "/".
func (t DeployPrefixTemplate) DeployPrefix(site sitekey.Slug, deployID string) string {
	p := strings.ReplaceAll(t.head, "<site>", string(site)) + deployID + t.tail
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}
