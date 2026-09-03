// Package docs owns authored and generated Markdown for the public documentation site.
package docs

import "embed"

// Files contains the unified catalog, search index, authored articles, generated
// references, downloadable schemas, and API contract.
//
//go:embed *
var Files embed.FS
