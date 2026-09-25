package migrations

import (
	"github.com/pressly/goose/v3"
	"testing"
)

func statusesThrough(applied, total int) []*goose.MigrationStatus {
	result := make([]*goose.MigrationStatus, 0, total)
	for i := 1; i <= total; i++ {
		state := goose.StatePending
		if i <= applied {
			state = goose.StateApplied
		}
		result = append(result, &goose.MigrationStatus{Source: &goose.Source{Version: int64(i)}, State: state})
	}
	return result
}
func TestUpgradeBoundaryRejectsUnknownTransitionsBeforeMigration(t *testing.T) {
	for _, pair := range [][2]int64{{0, 30}, {27, 30}, {30, 30}, {30, 28}, {28, 31}, {29, 30}} {
		if validateUpgradeBoundary(pair[0], pair[1], statusesThrough(int(pair[0]), 30)) == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
}
func TestUpgradeBoundaryRequiresCompleteHistoricalPrefix(t *testing.T) {
	s := statusesThrough(28, 30)
	if err := validateUpgradeBoundary(28, 30, s); err != nil {
		t.Fatal(err)
	}
	s[26].State = goose.StatePending
	if validateUpgradeBoundary(28, 30, s) == nil {
		t.Fatal("accepted missing historical migration")
	}
}
func TestUpgradeBoundaryRejectsPartialAndPrematureCandidate(t *testing.T) {
	for _, mutation := range []func([]*goose.MigrationStatus) []*goose.MigrationStatus{
		func(s []*goose.MigrationStatus) []*goose.MigrationStatus { return s[:29] },
		func(s []*goose.MigrationStatus) []*goose.MigrationStatus { s[29] = nil; return s },
		func(s []*goose.MigrationStatus) []*goose.MigrationStatus { s[29].State = goose.StateApplied; return s },
		func(s []*goose.MigrationStatus) []*goose.MigrationStatus { s[29].Source.Version = 31; return s },
	} {
		if validateUpgradeBoundary(28, 30, mutation(statusesThrough(28, 30))) == nil {
			t.Fatal("accepted inconsistent status")
		}
	}
}
