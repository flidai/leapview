package postgres

import (
	"context"

	releasedb "github.com/flidai/leapview/internal/release/postgres/internal/db"
)

type CredentialVersionReference struct{ Kind, ID string }

// CredentialVersionReferencesTx includes immutable candidate and release
// history. All generations, publications and rollback selections retain those
// same pins; a current-pointer-only check would lose these dependencies.
func (r *Repository) CredentialVersionReferencesTx(ctx context.Context, tx Tx, version string) ([]CredentialVersionReference, error) {
	rows, err := releasedb.New(tx).CredentialVersionReferences(ctx, version)
	if err != nil {
		return nil, err
	}
	result := make([]CredentialVersionReference, 0, len(rows))
	for _, row := range rows {
		result = append(result, CredentialVersionReference{Kind: row.ReferenceKind, ID: row.ReferenceID})
	}
	return result, nil
}
