package postgres

import _ "embed"

const (
	// AttributeRegistryMigrationRevision is the first forward-only extension to
	// the reconciled PostgreSQL control-plane baseline.
	AttributeRegistryMigrationRevision int64 = 2
	AttributeRegistryMigrationID             = "002_typed_attribute_registry"
)

//go:embed migrations/002_typed_attribute_registry.sql
var attributeRegistryMigrationSQL string

// AttributeRegistryMigrationSQL returns the access-owned immutable forward
// migration for application-level PostgreSQL migration composition.
func AttributeRegistryMigrationSQL() string { return attributeRegistryMigrationSQL }

const (
	// SemanticAttributeControlMigrationRevision adds durable subject assignments
	// and exact trusted-claim mappings without changing revision 002.
	SemanticAttributeControlMigrationRevision int64 = 3
	SemanticAttributeControlMigrationID             = "003_semantic_attribute_control"
)

//go:embed migrations/003_semantic_attribute_control.sql
var semanticAttributeControlMigrationSQL string

func SemanticAttributeControlMigrationSQL() string { return semanticAttributeControlMigrationSQL }
