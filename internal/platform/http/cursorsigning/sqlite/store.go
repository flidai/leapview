// Package sqlite loads the cursor signing key ring from durable instance state.
package sqlite

import (
	"context"
	"crypto/rand"
	"fmt"
	"reflect"
	"time"

	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	platformdb "github.com/flidai/leapview/internal/platform/http/cursorsigning/sqlite/cursordb"
)

// Initializer is the explicit SQLite fixture adapter for the engine-neutral
// cursor-signing startup port. Production protocol code should inject the
// PostgreSQL implementation instead.
type Initializer struct{ database platformdb.DBTX }

func NewInitializer(database platformdb.DBTX) Initializer { return Initializer{database: database} }

func (i Initializer) Configure(ctx context.Context) error {
	if i.database == nil || (reflect.ValueOf(i.database).Kind() == reflect.Ptr && reflect.ValueOf(i.database).IsNil()) {
		return fmt.Errorf("cursor signing SQLite database is nil")
	}
	return Configure(ctx, i.database)
}

func Configure(ctx context.Context, database platformdb.DBTX) error {
	queries := platformdb.New(database)
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("generate cursor signing key: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := queries.CreateInitialAPICursorSigningKey(ctx, platformdb.CreateInitialAPICursorSigningKeyParams{Secret: secret, CreatedAt: now}); err != nil {
		return fmt.Errorf("create cursor signing key: %w", err)
	}
	rows, err := queries.ListAPICursorSigningKeys(ctx)
	if err != nil {
		return fmt.Errorf("list cursor signing keys: %w", err)
	}
	keys := map[string][]byte{}
	current := ""
	for _, row := range rows {
		keys[row.KeyID] = append([]byte(nil), row.Secret...)
		if row.Active != 0 {
			current = row.KeyID
		}
	}
	return cursorsigning.Configure(current, keys)
}
