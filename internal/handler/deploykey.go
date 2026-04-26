package handler

import (
	"fmt"
	"strings"
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
func (t DeployPrefixTemplate) SitePrefix(site string) string {
	p := strings.ReplaceAll(t.head, "<site>", site)
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// DeployPrefix returns the R2 key prefix for one deploy, e.g.
// "www/deploys/20260420-141522-abc1234/". Always ends with "/".
func (t DeployPrefixTemplate) DeployPrefix(site, deployID string) string {
	p := strings.ReplaceAll(t.head, "<site>", site) + deployID + t.tail
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}
