// Package sitekey separates the two site keyspaces artemis carries.
//
// A Slug is what the registry stores and what every URL, JWT claim and
// audit row names. A Dirname is what R2 stores: the slug rendered
// through the site segment of DEPLOY_PREFIX_FORMAT, which under the
// production format appends the root domain.
//
// They are distinct types so the compiler, not review, catches one
// being passed where the other is meant. Conversion happens only
// through handler.DeployPrefixTemplate, which owns both directions.
package sitekey

type Slug string

type Dirname string
