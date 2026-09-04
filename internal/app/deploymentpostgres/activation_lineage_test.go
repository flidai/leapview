package deploymentpostgres

import (
	"errors"
	"strings"
	"testing"

	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestActivationLineageVerifierAdapterResolvesExactBinding(t *testing.T) {
	p := generationAdmissionDB(t)
	lineage := lineagepostgres.New(p)
	verifier, err := NewActivationLineageVerifier(lineage)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_lineage", Kind: projectgraph.KindProject, Name: "project"},
		{ID: "dashboard", Kind: projectgraph.KindDashboard, Name: "dashboard"},
	}, []projectgraph.Edge{{From: "project_lineage", To: "dashboard", Relation: "contains"}})
	if err != nil {
		t.Fatal(err)
	}
	const targetID = "target-lineage"
	const generationID = "generation-lineage"
	const projectID = "project_lineage"
	projection, err := lineagepostgres.FromGraph(graph)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lineage.PersistGraph(t.Context(), tx, graph, lineagepostgres.Binding{
		DeliveryID: targetID, GenerationID: generationID, ProjectID: projectID,
	}); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	verify := func(input deploymentnative.ActivationLineageInput) error {
		t.Helper()
		readTx, err := p.Begin(t.Context())
		if err != nil {
			return err
		}
		defer readTx.Rollback(t.Context())
		return verifier.VerifyActivationLineage(t.Context(), readTx, input)
	}
	if err := verify(deploymentnative.ActivationLineageInput{TargetID: targetID, ProjectID: projectID, GenerationID: generationID, CompiledGraphDigest: projection.Digest}); err != nil {
		t.Fatalf("exact activation lineage binding rejected: %v", err)
	}
	if err := verify(deploymentnative.ActivationLineageInput{TargetID: targetID, ProjectID: projectID, GenerationID: generationID, CompiledGraphDigest: "sha256:" + "0" + strings.Repeat("1", 63)}); err == nil || !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("mismatched activation lineage digest error = %v, want deployment conflict", err)
	}
	for name, input := range map[string]deploymentnative.ActivationLineageInput{
		"wrong project":    {TargetID: targetID, ProjectID: "project_other", GenerationID: generationID},
		"wrong target":     {TargetID: "target-other", ProjectID: projectID, GenerationID: generationID},
		"wrong generation": {TargetID: targetID, ProjectID: projectID, GenerationID: "generation-other"},
		"missing project":  {TargetID: targetID, GenerationID: generationID, CompiledGraphDigest: projection.Digest},
	} {
		t.Run(name, func(t *testing.T) {
			if err := verify(input); err == nil || !errors.Is(err, deploymentnative.ErrConflict) {
				t.Fatalf("activation lineage verification error = %v, want deployment conflict", err)
			}
		})
	}
}

func TestNewActivationLineageVerifierRejectsUnconfiguredRepository(t *testing.T) {
	if verifier, err := NewActivationLineageVerifier(nil); err == nil || verifier != nil {
		t.Fatalf("nil lineage repository = verifier %v, err %v; want constructor rejection", verifier, err)
	}
	if verifier, err := NewActivationLineageVerifier(lineagepostgres.New(nil)); err == nil || verifier != nil {
		t.Fatalf("unconfigured lineage repository = verifier %v, err %v; want constructor rejection", verifier, err)
	}
}
