package postgres

import _ "embed"

// schemaSQL is the capability-owned PostgreSQL DDL. The control-plane
// migration runner may apply it as a forward capability migration.
//
//go:embed schema.sql
var schemaSQL string

// SchemaSQL returns the capability-owned PostgreSQL DDL. It contains no
// transaction control so migration callers decide the commit boundary.
func SchemaSQL() string { return schemaSQL }
