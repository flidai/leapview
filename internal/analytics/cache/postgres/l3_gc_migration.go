package postgres

import _ "embed"

const (
	// L3GCMigrationRevision is the forward-only cache migration that adds
	// durable object fencing and pool-scoped orphan collection.
	L3GCMigrationRevision int64 = 4
	L3GCMigrationID             = "004_l3_object_gc"
)

//go:embed migrations/004_l3_object_gc.sql
var l3GCMigrationSQL string

// L3GCMigrationSQL returns the immutable cache-owned forward migration.
func L3GCMigrationSQL() string { return l3GCMigrationSQL }
