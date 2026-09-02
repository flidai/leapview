package runtimehost

import (
	"errors"
	"testing"

	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestValidateCandidateDataModeRefreshSnapshotSemantics(t *testing.T) {
	baseCompatibility := CandidateCompatibility{
		DataMode:               CandidateDataRefreshSources,
		ManagedDataConnections: []string{"warehouse"},
	}
	managedData := ManagedDataResolution{Roots: map[string]string{"warehouse": "/tmp/warehouse"}}

	tests := []struct {
		name                 string
		requireSealedCatalog bool
		snapshotID           int64
		wantErr              bool
	}{
		{name: "sealed positive snapshot", requireSealedCatalog: true, snapshotID: 42},
		{name: "sealed zero snapshot", requireSealedCatalog: true, snapshotID: 0, wantErr: true},
		{name: "unsealed zero snapshot", snapshotID: 0},
		{name: "unsealed positive snapshot", snapshotID: 42, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := servingstate.State{DuckLakeSnapshotID: test.snapshotID}
			err := validateCandidateDataMode(state, baseCompatibility, managedData, test.requireSealedCatalog)
			if test.wantErr {
				if !errors.Is(err, ErrCandidateRuntimeIncompatible) {
					t.Fatalf("error = %v, want candidate runtime incompatible", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateCandidateDataModeRefreshRequiresDeclaredConnection(t *testing.T) {
	for _, test := range []struct {
		name                 string
		requireSealedCatalog bool
		snapshotID           int64
	}{
		{name: "sealed", requireSealedCatalog: true, snapshotID: 42},
		{name: "unsealed", snapshotID: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			compatibility := CandidateCompatibility{DataMode: CandidateDataRefreshSources}
			err := validateCandidateDataMode(servingstate.State{DuckLakeSnapshotID: test.snapshotID}, compatibility, ManagedDataResolution{}, test.requireSealedCatalog)
			if !errors.Is(err, ErrCandidateRuntimeIncompatible) {
				t.Fatalf("error = %v, want candidate runtime incompatible", err)
			}
		})
	}
}
