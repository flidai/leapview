package duckdbsql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2"
)

// Parser is an application-neutral DuckDB SQL parser. Each Parse call uses a
// fresh in-memory connection; no caller data, bindings, or connection state
// can leak between requests.
type Parser struct{ limits Limits }

func New(options Limits) (*Parser, error) {
	limits, err := options.normalized()
	if err != nil {
		return nil, err
	}
	return &Parser{limits: limits}, nil
}

func Parse(ctx context.Context, sqlText string) (Query, error) {
	p, err := New(Limits{})
	if err != nil {
		return Query{}, err
	}
	return p.Parse(ctx, sqlText)
}

func (p *Parser) Parse(ctx context.Context, sqlText string) (Query, error) {
	if p == nil {
		return Query{}, &ParseError{Kind: ErrorConfiguration, Message: "nil parser", BytePosition: -1}
	}
	if len(sqlText) > p.limits.MaxSQLBytes {
		return Query{}, limitError(fmt.Sprintf("SQL input exceeds %d bytes", p.limits.MaxSQLBytes))
	}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return Query{}, &ParseError{Kind: ErrorConfiguration, Message: "open parser connection", BytePosition: -1, Cause: err}
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// Restrictive settings are established before loading the bundled JSON
	// extension and locked before user SQL is serialized.
	for _, statement := range []string{
		"SET enable_external_access = false",
		"SET autoload_known_extensions = false",
		"SET autoinstall_known_extensions = false",
		"SET allow_persistent_secrets = false",
		"LOAD json",
		"SET enable_external_access = false",
		"SET autoload_known_extensions = false",
		"SET autoinstall_known_extensions = false",
		"SET allow_persistent_secrets = false",
		"SET lock_configuration = true",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return Query{}, &ParseError{Kind: ErrorConfiguration, Message: "configure parser connection", BytePosition: -1, Cause: err}
		}
	}
	var payload string
	err = db.QueryRowContext(ctx, "SELECT CAST(json_serialize_sql(CAST(? AS VARCHAR), skip_default := true, skip_empty := true, skip_null := true) AS VARCHAR)", sqlText).Scan(&payload)
	if err != nil {
		return Query{}, &ParseError{Kind: ErrorSyntax, Message: "serialize SQL", BytePosition: -1, Cause: err}
	}
	if len(payload) > p.limits.MaxJSONBytes {
		return Query{}, limitError(fmt.Sprintf("serialized SQL exceeds %d bytes", p.limits.MaxJSONBytes))
	}
	return decodeDocument([]byte(payload), sqlText, p.limits)
}

func (p *Parser) Limits() Limits {
	if p == nil {
		return Limits{}
	}
	return p.limits
}

func (p *Parser) String() string {
	if p == nil {
		return "duckdbsql.Parser{nil}"
	}
	return "duckdbsql.Parser{" + strings.TrimSpace(fmt.Sprintf("max_sql=%d", p.limits.MaxSQLBytes)) + "}"
}
