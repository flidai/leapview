package runtimefactory

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/servingstate"
)

func TestBindManagedDataRootsUsesTrustedRuntimeResolution(t *testing.T) {
	definition := &manifest.Project{NameIndex: manifest.NameIndex{Connections: map[string]string{"olist": "connection:olist", "cloud": "connection:cloud"}}, SemanticModels: map[string]*semanticmodel.Model{
		"sales": {Connections: map[string]semanticmodel.Connection{
			"olist": {Kind: "managed"},
			"cloud": {Kind: "s3", Scope: "s3://warehouse/"},
		}},
	}}
	resolution := runtimehost.ManagedDataResolution{
		RevisionID: "sha256:" + strings.Repeat("a", 64),
		Roots:      map[string]string{"connection:olist": "/managed/olist/revision"},
	}
	if err := bindManagedDataRoots(definition, resolution.Roots); err != nil {
		t.Fatal(err)
	}
	if got := definition.SemanticModels["sales"].Connections["olist"].Root; got != "/managed/olist/revision" {
		t.Fatalf("olist root = %q", got)
	}
	if got := definition.SemanticModels["sales"].Connections["cloud"].Scope; got != "s3://warehouse/" {
		t.Fatalf("cloud scope = %q", got)
	}
}

func TestBindManagedDataRootsRequiresEveryManagedConnection(t *testing.T) {
	definition := &manifest.Project{NameIndex: manifest.NameIndex{Connections: map[string]string{"olist": "connection:olist"}}, SemanticModels: map[string]*semanticmodel.Model{
		"sales": {Connections: map[string]semanticmodel.Connection{"olist": {Kind: "managed"}}},
	}}
	err := bindManagedDataRoots(definition, nil)
	if err == nil || !strings.Contains(err.Error(), "olist") {
		t.Fatalf("bind error = %v, want missing olist revision", err)
	}
}

func TestRuntimeExtractionIdentitySeparatesCandidateFromActiveState(t *testing.T) {
	active := runtimeExtractionIdentity(runtimehost.RuntimeInput{
		State: servingstate.State{ID: "state_sales"},
	})
	candidate := runtimeExtractionIdentity(runtimehost.RuntimeInput{
		State: servingstate.State{ID: "state_sales"},
		Candidate: &runtimehost.CandidateRuntimeContext{
			CandidateID: "cand_1",
		},
	})
	if active != "state_sales" {
		t.Fatalf("active extraction identity = %q", active)
	}
	if candidate == "" || candidate == active {
		t.Fatalf(
			"candidate extraction identity = %q, active = %q",
			candidate,
			active,
		)
	}
}
