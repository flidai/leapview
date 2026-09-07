package module

import (
	"context"
	"errors"
	"testing"
)

type catalogProjectStub struct{ configured bool }

func (catalogProjectStub) PostgreSQLAuthority() {}
func (s catalogProjectStub) Configured() bool   { return s.configured }
func (catalogProjectStub) GetProject(context.Context, string) (ProjectIdentityRecord, error) {
	return ProjectIdentityRecord{ID: "commerce", CreatedAt: "created", UpdatedAt: "updated"}, nil
}

type catalogBindingStub struct {
	configured bool
	rows       []ConnectionBindingRecord
}

func (catalogBindingStub) PostgreSQLAuthority() {}
func (s catalogBindingStub) Configured() bool   { return s.configured }
func (s catalogBindingStub) ListConnections(context.Context, string, string, string) ([]ConnectionBindingRecord, error) {
	return append([]ConnectionBindingRecord(nil), s.rows...), nil
}
func (s catalogBindingStub) GetConnection(context.Context, string, string, string, string) (ConnectionBindingRecord, error) {
	if len(s.rows) == 0 {
		return ConnectionBindingRecord{}, errors.New("missing")
	}
	return s.rows[0], nil
}

func TestNewPostgresCatalogRequiresPointerReaders(t *testing.T) {
	_, err := NewPostgresCatalog(PostgresCatalogConfig{
		Projects: catalogProjectStub{configured: true}, Bindings: catalogBindingStub{configured: true}, TargetID: "target:dev",
	})
	if err == nil {
		t.Fatal("expected pointer-reader validation")
	}
}

func TestPostgresCatalogMapsAndSortsNativeRows(t *testing.T) {
	catalog, err := NewPostgresCatalog(PostgresCatalogConfig{
		Projects:           catalogProjectStub{configured: true},
		Bindings:           catalogBindingStub{configured: true, rows: []ConnectionBindingRecord{{ConnectionID: "connection:z", Title: "Z"}, {ConnectionID: "connection:a", Title: "A"}}},
		TargetID:           "target:dev",
		LatestReleaseID:    func(context.Context, string) (string, error) { return "release:latest", nil },
		ActiveDeploymentID: func(context.Context, string) (string, error) { return "deployment:active", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := catalog.GetProject(t.Context(), "commerce")
	if err != nil || project.LatestReleaseID != "release:latest" || project.ActiveDeploymentID != "deployment:active" {
		t.Fatalf("project = %#v, err = %v", project, err)
	}
	rows, err := catalog.ListConnections(t.Context(), "commerce", "dev")
	if err != nil || len(rows) != 2 || rows[0].ID != "connection:a" || rows[1].ID != "connection:z" {
		t.Fatalf("rows = %#v, err = %v", rows, err)
	}
}
