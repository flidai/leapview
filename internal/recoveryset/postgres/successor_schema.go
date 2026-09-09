package postgres

import (
	"embed"
	"fmt"
)

// successorSchemaFS keeps the additive RecoverySet v3 DDL beside the
// recovery owner package.  It is inspection/application material only; no
// runtime admission path calls it.
//
//go:embed successor_schema.sql
var successorSchemaFS embed.FS

// SuccessorSchemaSQL returns the reviewed additive v3 schema source. Goose
// migration 005 is the deployment authority; this copy is used by schema
// parity checks and owner-side tooling.
func SuccessorSchemaSQL() string {
	b, err := successorSchemaFS.ReadFile("successor_schema.sql")
	if err != nil {
		panic(fmt.Sprintf("read successor recovery schema: %v", err))
	}
	return string(b)
}
