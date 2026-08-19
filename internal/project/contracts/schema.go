package contracts

import _ "embed"

// DataResourcesSchema is the generated Draft 2020-12 structural contract for
// Connection, Source, and Model resource documents. Contextual project rules
// remain in internal/project/schema; this package owns the public shape.
//
//go:embed gen/data-resources.schema.json
var DataResourcesSchema []byte
