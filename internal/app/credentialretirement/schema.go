// Package credentialretirement owns application-level database dependency
// fences connecting credential versions to release and agent history.
package credentialretirement

import _ "embed"

//go:embed schema.sql
var schema string

// SchemaSQL is used by focused composition fixtures. Production applies the
// identical reviewed source through the forward-only migration.
func SchemaSQL() string { return schema }
