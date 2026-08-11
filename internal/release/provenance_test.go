package release

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProvenanceSeparatesImmutableArtifactFromTargetPlan(t *testing.T) {
	first, err := NewProvenance(provenanceInput("target-dev", "dev", "a"))
	require.NoError(t, err)
	reordered := provenanceInput("target-dev", "dev", "a")
	reordered.Artifact.Workspaces[0], reordered.Artifact.Workspaces[1] =
		reordered.Artifact.Workspaces[1], reordered.Artifact.Workspaces[0]
	reordered.Plan.Workspaces[0], reordered.Plan.Workspaces[1] =
		reordered.Plan.Workspaces[1], reordered.Plan.Workspaces[0]
	second, err := NewProvenance(reordered)
	require.NoError(t, err)
	if first.Digest != second.Digest ||
		first.ArtifactDigest != second.ArtifactDigest ||
		first.PlanDigest != second.PlanDigest {
		t.Fatalf("canonical provenance changed with input order: %#v / %#v", first, second)
	}

	promoted, err := NewProvenance(provenanceInput("target-prod", "prod", "b"))
	require.NoError(t, err)
	if promoted.ArtifactDigest != first.ArtifactDigest {
		t.Fatalf("promotion changed artifact digest: %q / %q", first.ArtifactDigest, promoted.ArtifactDigest)
	}
	if promoted.PlanDigest == first.PlanDigest || promoted.Digest == first.Digest {
		t.Fatalf("target-specific plan did not change release identity: %#v / %#v", first, promoted)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestProvenanceAllowsAuthoredOnlySourceRefresh(t *testing.T) {
	input := provenanceInput("target-dev", "dev", "a")
	workspace := &input.Plan.Workspaces[1]
	workspace.Bindings = nil
	workspace.ManagedDataPins = nil
	workspace.AuthoredConnections = []AuthoredConnectionEvidence{{
		LogicalConnection: "public_http", ConnectorKind: "http",
	}}

	provenance, err := NewProvenance(input)
	require.NoError(t, err)
	require.Equal(
		t,
		workspace.AuthoredConnections,
		provenance.Plan.Workspaces[0].AuthoredConnections,
	)
}

func TestProvenanceRetainsBindingEvidenceForSnapshotReuse(t *testing.T) {
	input := provenanceInput("target-dev", "dev", "a")
	workspace := &input.Plan.Workspaces[0]
	workspace.Bindings = []BindingEvidence{{
		BindingID: "warehouse", LogicalConnection: "warehouse",
		ConnectorKind: "postgres", Revision: 2,
		ValidatedVersion: "version-9", EndpointConfigHash: shaIdentity("8"),
	}}

	provenance, err := NewProvenance(input)
	require.NoError(t, err)
	var retained []BindingEvidence
	for _, planned := range provenance.Plan.Workspaces {
		if planned.WorkspaceID == workspace.WorkspaceID {
			retained = planned.Bindings
		}
	}
	require.Equal(t, workspace.Bindings, retained)
}

func TestProvenanceBindsOptionalSourceRevisionWithoutChangingArtifactIdentity(t *testing.T) {
	withoutRevision, err := NewProvenance(provenanceInput("target-dev", "dev", "a"))
	require.NoError(t, err)
	input := provenanceInput("target-dev", "dev", "a")
	input.SourceRevision = &SourceRevisionProvenance{
		Revision:   "0123456789abcdef",
		Repository: "https://code.example/acme/analytics",
		Ref:        "refs/pull/42/merge",
		ChangeID:   "pull/42",
	}
	withRevision, err := NewProvenance(input)
	require.NoError(t, err)
	if withRevision.ArtifactDigest != withoutRevision.ArtifactDigest {
		t.Fatalf("source revision changed artifact identity: %q / %q", withRevision.ArtifactDigest, withoutRevision.ArtifactDigest)
	}
	if withRevision.PlanDigest == withoutRevision.PlanDigest ||
		withRevision.Digest == withoutRevision.Digest {
		t.Fatalf("source revision was not bound to release identity: %#v / %#v", withRevision, withoutRevision)
	}
	if withRevision.SourceRevision == nil ||
		withRevision.SourceRevision.Revision != input.SourceRevision.Revision {
		t.Fatalf("source revision = %#v, want %#v", withRevision.SourceRevision, input.SourceRevision)
	}
	if err := withRevision.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestProvenanceRejectsUnsafeSourceRevisionEvidence(t *testing.T) {
	for name, sourceRevision := range map[string]*SourceRevisionProvenance{
		"missing revision": {Repository: "https://code.example/acme/analytics"},
		"control character": {
			Revision: "abc\nsecret",
		},
		"credential URL": {
			Revision:   "abc123",
			Repository: "https://token:secret@code.example/acme/analytics",
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := provenanceInput("target-dev", "dev", "a")
			input.SourceRevision = sourceRevision
			if _, err := NewProvenance(input); !errors.Is(err, ErrProvenanceInvalid) {
				t.Fatalf("NewProvenance() error = %v, want ErrProvenanceInvalid", err)
			}
		})
	}
}

func TestProvenanceFailsClosedOnTamperingAndIncompleteCompatibility(t *testing.T) {
	valid, err := NewProvenance(provenanceInput("target-dev", "dev", "a"))
	require.NoError(t, err)
	tampered := valid
	tampered.Artifact.ProjectDigest = shaIdentity("f")
	if err := tampered.Validate(); !errors.Is(err, ErrProvenanceInvalid) {
		t.Fatalf("tampered provenance error = %v, want ErrProvenanceInvalid", err)
	}

	tests := map[string]func(*ProvenanceInput){
		"compiler version": func(input *ProvenanceInput) {
			input.Artifact.CompilerVersion = ""
		},
		"artifact schema": func(input *ProvenanceInput) {
			input.Artifact.SchemaVersion = 0
		},
		"managed data pin": func(input *ProvenanceInput) {
			input.Plan.Workspaces[0].ManagedDataPins[0].RevisionID = ""
		},
		"binding evidence": func(input *ProvenanceInput) {
			input.Plan.Workspaces[1].Bindings[0].ValidatedVersion = ""
		},
		"runtime version": func(input *ProvenanceInput) {
			input.Plan.RuntimeVersion = ""
		},
		"policy digest": func(input *ProvenanceInput) {
			input.Plan.PolicyDigest = ""
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := provenanceInput("target-dev", "dev", "a")
			mutate(&input)
			if _, err := NewProvenance(input); !errors.Is(err, ErrProvenanceInvalid) {
				t.Fatalf("NewProvenance() error = %v, want ErrProvenanceInvalid", err)
			}
		})
	}
}

func TestProvenanceSerializesOnlyRedactedTargetEvidence(t *testing.T) {
	provenance, err := NewProvenance(provenanceInput("target-dev", "dev", "a"))
	require.NoError(t, err)
	encoded, err := json.Marshal(provenance)
	require.NoError(t, err)
	for _, forbidden := range []string{
		"infisicalProject", "secretPath", "secretKey", "credential",
		"postgres://", "super-secret",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("provenance contains forbidden provider or secret material %q: %s", forbidden, encoded)
		}
	}
}

func TestProvenanceRejectsPreviousVersionWithResetInstruction(t *testing.T) {
	provenance, err := NewProvenance(provenanceInput("target-dev", "dev", "a"))
	require.NoError(t, err)
	require.Equal(t, 3, ProvenanceVersion)

	provenance.Version = 2
	err = provenance.Validate()
	require.ErrorIs(t, err, ErrProvenanceInvalid)
	require.Contains(t, err.Error(), "reset target state")
}

func provenanceInput(targetID, environment, suffix string) ProvenanceInput {
	return ProvenanceInput{
		Artifact: ProjectArtifactProvenance{
			SourceDigest:    shaIdentity("1"),
			ProjectDigest:   shaIdentity("2"),
			CompilerVersion: "leapview:1.2.3",
			SchemaVersion:   3,
			Workspaces: []WorkspaceArtifactProvenance{
				{WorkspaceID: "sales", ArtifactDigest: shaIdentity("3")},
				{WorkspaceID: "operations", ArtifactDigest: shaIdentity("4")},
			},
		},
		Candidate: CandidateProvenance{
			ID: "cand_1", Revision: 7, OwnerID: "principal_1",
		},
		Plan: TargetPlanProvenance{
			TargetID: targetID, Environment: environment,
			BaseGeneration: "generation-" + suffix,
			RuntimeVersion: "leapview-runtime:1.2.3",
			PolicyDigest:   shaIdentity("5"),
			Workspaces: []TargetWorkspacePlan{
				{
					WorkspaceID: "sales", ServingStateID: "state-sales-" + suffix,
					ArtifactDigest: shaIdentity(suffix), DataRevision: "snapshot:42",
					DataMode: TargetDataReuseSnapshot,
					ManagedDataPins: []ManagedDataPin{
						{ConnectionID: "warehouse", RevisionID: shaIdentity("6")},
					},
				},
				{
					WorkspaceID: "operations", ServingStateID: "state-operations-" + suffix,
					ArtifactDigest: shaIdentity(suffix), DataRevision: "sources:" + shaIdentity("1"),
					DataMode: TargetDataRefreshSources,
					ManagedDataPins: []ManagedDataPin{
						{ConnectionID: "warehouse", RevisionID: shaIdentity("7")},
					},
					Bindings: []BindingEvidence{
						{
							BindingID: "warehouse", LogicalConnection: "warehouse",
							ConnectorKind: "postgres", Revision: 2,
							ValidatedVersion: "version-9", EndpointConfigHash: shaIdentity("8"),
						},
					},
				},
			},
		},
	}
}

func shaIdentity(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
