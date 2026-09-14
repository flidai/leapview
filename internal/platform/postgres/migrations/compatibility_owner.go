package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// ControlCompatibilityState is the read-only Goose owner's exact installed
// and embedded migration boundary. Compatibility is deliberately conservative:
// any pending forward migration requires provider recovery for binary rollback.
type ControlCompatibilityState struct {
	InstalledRevision int64
	EmbeddedRevision  int64
}

// RiverJobsCompatibilityState combines River's upstream schema owner with the
// LeapView-owned product job-history schema. The latter is installed by Goose,
// so its authoritative revision is the same applied control migration fence.
type RiverJobsCompatibilityState struct {
	InstalledRiverVersions []int
	EmbeddedRiverVersions  []int
	InstalledJobsRevision  int64
	EmbeddedJobsRevision   int64
}

// ReadControlCompatibilityState inspects Goose without applying migrations.
// A partial, out-of-order, or ahead database is not evidence and fails closed.
func ReadControlCompatibilityState(ctx context.Context, db *sql.DB) (ControlCompatibilityState, error) {
	if db == nil {
		return ControlCompatibilityState{}, errors.New("PostgreSQL Goose compatibility database is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	provider, err := NewProvider(db)
	if err != nil {
		return ControlCompatibilityState{}, err
	}
	installed, embedded, err := provider.GetVersions(ctx)
	if err != nil {
		return ControlCompatibilityState{}, fmt.Errorf("read PostgreSQL Goose compatibility versions: %w", err)
	}
	if installed < BaselineRevision || embedded != CurrentRevision || installed > embedded {
		return ControlCompatibilityState{}, fmt.Errorf("PostgreSQL Goose compatibility state %d/%d is unsupported", installed, embedded)
	}
	statuses, err := provider.Status(ctx)
	if err != nil {
		return ControlCompatibilityState{}, fmt.Errorf("inspect PostgreSQL Goose compatibility status: %w", err)
	}
	for _, status := range statuses {
		if status == nil || status.Source == nil {
			return ControlCompatibilityState{}, errors.New("PostgreSQL Goose compatibility status is incomplete")
		}
		want := goose.StatePending
		if status.Source.Version <= installed {
			want = goose.StateApplied
		}
		if status.State != want {
			return ControlCompatibilityState{}, fmt.Errorf("PostgreSQL Goose migration %d has state %s, want %s", status.Source.Version, status.State, want)
		}
	}
	return ControlCompatibilityState{InstalledRevision: installed, EmbeddedRevision: embedded}, nil
}

// ReadRiverJobsCompatibilityState reads River's own migration ledger and the
// Goose fence that owns LeapView job history. It never executes either owner.
func ReadRiverJobsCompatibilityState(ctx context.Context, pool *pgxpool.Pool, db *sql.DB) (RiverJobsCompatibilityState, error) {
	if pool == nil {
		return RiverJobsCompatibilityState{}, errors.New("PostgreSQL River compatibility pool is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return RiverJobsCompatibilityState{}, fmt.Errorf("construct River compatibility owner: %w", err)
	}
	existing, err := migrator.ExistingVersions(ctx)
	if err != nil {
		return RiverJobsCompatibilityState{}, fmt.Errorf("read River compatibility versions: %w", err)
	}
	all := migrator.AllVersions()
	installed := make([]int, len(existing))
	embedded := make([]int, len(all))
	for i := range existing {
		installed[i] = existing[i].Version
	}
	for i := range all {
		embedded[i] = all[i].Version
	}
	if len(installed) == 0 || len(installed) > len(embedded) || !slices.Equal(installed, embedded[:len(installed)]) {
		return RiverJobsCompatibilityState{}, fmt.Errorf("River compatibility state %v/%v is missing or ambiguous", installed, embedded)
	}
	control, err := ReadControlCompatibilityState(ctx, db)
	if err != nil {
		return RiverJobsCompatibilityState{}, fmt.Errorf("read LeapView job-history compatibility: %w", err)
	}
	return RiverJobsCompatibilityState{
		InstalledRiverVersions: append([]int(nil), installed...),
		EmbeddedRiverVersions:  append([]int(nil), embedded...),
		InstalledJobsRevision:  control.InstalledRevision,
		EmbeddedJobsRevision:   control.EmbeddedRevision,
	}, nil
}

// CanonicalMigrationVersions renders owner-read version state without map or
// iteration-order ambiguity.
func CanonicalMigrationVersions(owner string, versions []int) (string, error) {
	if owner == "" || owner != strings.TrimSpace(owner) || len(versions) == 0 {
		return "", errors.New("migration compatibility version state is invalid")
	}
	parts := make([]string, len(versions))
	for i, version := range versions {
		if version <= 0 || (i > 0 && version <= versions[i-1]) {
			return "", errors.New("migration compatibility versions are not canonical")
		}
		parts[i] = strconv.Itoa(version)
	}
	return owner + "/[" + strings.Join(parts, ",") + "]", nil
}
