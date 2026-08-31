package refreshpostgres

import (
	"context"
	"testing"

	postgresrepo "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

func TestPostgresNativeRefreshFinalizerRejectsWhitespaceResolverResult(t *testing.T) {
	f := &PostgresNativeRefreshFinalizerAdapter{TargetResolver: PostgresNativeRefreshTargetResolverFunc(func(context.Context, postgresrepo.Tx, refreshrun.JobRecord) (string, error) {
		return " target", nil
	})}
	if _, err := f.resolveTarget(t.Context(), nil, refreshrun.JobRecord{}); err == nil {
		t.Fatal("whitespace target resolver result unexpectedly accepted")
	}
}
