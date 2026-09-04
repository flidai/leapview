package module

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
)

func TestNativeGenerationBaseLoadsExactObjectAndPlansAgainstArtifact(t *testing.T) {
	fixture := nativeInspectFixture(t)
	base := nativeBaseFixture(t, fixture)
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, states: nativeBaseStateStub{state: base.state, artifact: base.artifact}, provenance: nativeBaseProvenanceStub{provenance: base.provenance}, artifacts: base.store, storageDomain: "runtime", environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	loaded, err := service.nativeGenerationBase(t.Context(), &base.identity)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.active || loaded.artifact.Digest() != base.state.ProjectDigest || loaded.dataRevision != "snapshot:17" || loaded.snapshotID != 17 {
		t.Fatalf("loaded native base = %#v", loaded)
	}
	request := fixture.request
	request.Scope.BaseGenerationID = base.identity.GenerationID
	inspected, err := service.InspectCandidateArtifacts(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Generation.DataMode != release.GenerationDataReuseBase || inspected.Generation.DataRevision != "snapshot:17" {
		t.Fatalf("inspect data evidence = %q/%q", inspected.Generation.DataMode, inspected.Generation.DataRevision)
	}
	if inspected.Compiler.Plan.Summary.Added != 0 || inspected.Compiler.Plan.Summary.Removed != 0 {
		t.Fatalf("plan was not compared to exact base artifact: %#v", inspected.Compiler.Plan.Summary)
	}
}

func TestNativeGenerationBaseHydratesReorderedJSONBManifest(t *testing.T) {
	fixture := nativeInspectFixture(t)
	base := nativeBaseFixture(t, fixture)
	reorderObject := func(label, value string) string {
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(value), &object); err != nil {
			t.Fatalf("unmarshal %s JSON: %v", label, err)
		}
		reordered, err := json.Marshal(object)
		if err != nil {
			t.Fatalf("marshal reordered %s JSON: %v", label, err)
		}
		return string(reordered)
	}
	reordered := reorderObject("base manifest", base.artifact.ManifestJSON)
	if string(reordered) == base.artifact.ManifestJSON {
		t.Fatal("reordered base manifest JSON unexpectedly retained the original key order")
	}
	// Serving-state JSONB can reorder object keys, while the immutable bundle
	// retains the producer's original manifest bytes.
	base.state.ManifestJSON = string(reordered)
	base.artifact.ManifestJSON = string(reordered)
	base.state.AccessPolicyJSON = reorderObject("access policy", base.state.AccessPolicyJSON)
	base.state.DashboardPublicationsJSON = reorderObject("dashboard publications", base.state.DashboardPublicationsJSON)
	base.state.DashboardAppearancesJSON = reorderObject("dashboard appearances", base.state.DashboardAppearancesJSON)

	service := &nativeCandidateArtifactPhases{
		states:        nativeBaseStateStub{state: base.state, artifact: base.artifact},
		provenance:    nativeBaseProvenanceStub{provenance: base.provenance},
		artifacts:     base.store,
		storageDomain: "runtime",
		environment:   "dev",
	}
	loaded, err := service.nativeGenerationBase(t.Context(), &base.identity)
	if err != nil {
		t.Fatalf("nativeGenerationBase() error = %v, want success for semantically equivalent manifest", err)
	}
	if !loaded.active || loaded.artifact.Digest() != base.state.ProjectDigest {
		t.Fatalf("loaded native base = %#v", loaded)
	}
}

func TestNativeGenerationBaseRejectsInactiveOrIncompleteState(t *testing.T) {
	fixture := nativeInspectFixture(t)
	base := nativeBaseFixture(t, fixture)
	service := &nativeCandidateArtifactPhases{states: nativeBaseStateStub{state: base.state, artifact: base.artifact}, provenance: nativeBaseProvenanceStub{provenance: base.provenance}, artifacts: base.store, storageDomain: "runtime", environment: "dev"}
	for name, mutate := range map[string]func(*servingstate.State){
		"inactive": func(state *servingstate.State) { state.Status = servingstate.StatusValidated },
		"snapshot": func(state *servingstate.State) { state.DuckLakeSnapshotID = 0 },
	} {
		state := base.state
		mutate(&state)
		service.states = nativeBaseStateStub{state: state, artifact: base.artifact}
		if _, err := service.nativeGenerationBase(t.Context(), &base.identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
			t.Fatalf("%s state error = %v", name, err)
		}
	}
}

func TestNativeGenerationBaseClassifiesPermanentProvenanceFailuresAsInvalid(t *testing.T) {
	fixture := nativeInspectFixture(t)
	base := nativeBaseFixture(t, fixture)
	for _, provenanceErr := range []error{release.ErrConflict, release.ErrInvalid, release.ErrProvenanceInvalid} {
		service := &nativeCandidateArtifactPhases{states: nativeBaseStateStub{state: base.state, artifact: base.artifact}, provenance: nativeBaseProvenanceStub{err: provenanceErr}, artifacts: base.store, storageDomain: "runtime", environment: "dev"}
		if _, err := service.nativeGenerationBase(t.Context(), &base.identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
			t.Fatalf("provenance error %v classified as %v, want candidate artifact invalid", provenanceErr, err)
		}
	}
}

func TestNativeGenerationBaseRejectsObjectAndManifestEvidence(t *testing.T) {
	fixture := nativeInspectFixture(t)
	base := nativeBaseFixture(t, fixture)
	service := &nativeCandidateArtifactPhases{states: nativeBaseStateStub{state: base.state, artifact: base.artifact}, provenance: nativeBaseProvenanceStub{provenance: base.provenance}, artifacts: base.store, storageDomain: "runtime", environment: "dev"}
	for name, mutate := range map[string]func(*servingstate.Artifact){
		"locator":  func(artifact *servingstate.Artifact) { artifact.Locator = "serving-artifacts/forged.tar.gz" },
		"metadata": func(artifact *servingstate.Artifact) { artifact.MetadataDigest = testNativeDigest("forged") },
		"manifest": func(artifact *servingstate.Artifact) { artifact.ManifestJSON = `{}` },
	} {
		artifact := base.artifact
		mutate(&artifact)
		service.states = nativeBaseStateStub{state: base.state, artifact: artifact}
		if _, err := service.nativeGenerationBase(t.Context(), &base.identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
			t.Fatalf("%s artifact error = %v", name, err)
		}
	}
	service.states = nativeBaseStateStub{state: base.state, artifact: base.artifact}
	service.artifacts = &nativeForgedArtifactStore{ImmutableStore: base.store, body: []byte("corrupt bundle")}
	if _, err := service.nativeGenerationBase(t.Context(), &base.identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("corrupt object error = %v", err)
	}
}

func TestNativeGenerationBaseRejectsDuplicateBindingEvidence(t *testing.T) {
	fixture := nativeInspectFixture(t)
	base := nativeBaseFixture(t, fixture)
	duplicate := base.provenance
	duplicate.Plan.Bindings = []release.BindingEvidence{{BindingID: "binding-a", ConnectionID: "connection:warehouse", ConnectorKind: "managed", Revision: 1, ValidatedVersion: "v1", EndpointConfigHash: testNativeDigest("a")}, {BindingID: "binding-b", ConnectionID: "connection:warehouse", ConnectorKind: "managed", Revision: 2, ValidatedVersion: "v2", EndpointConfigHash: testNativeDigest("b")}}
	gate := *duplicate.Plan.GateEvidence
	gate.BindingGeneration = release.BindingFingerprint(duplicate.Plan.Bindings)
	canonicalGate, err := gate.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	duplicate.Plan.GateEvidence = &canonicalGate
	// Recompute the immutable provenance envelope so this reaches the loader's
	// uniqueness check rather than being rejected as a stale content digest.
	duplicate, err = release.NewProvenance(release.ProvenanceInput{Artifact: duplicate.Artifact, Candidate: duplicate.Candidate, SourceRevision: duplicate.SourceRevision, Plan: duplicate.Plan})
	if err != nil {
		t.Fatal(err)
	}
	service := &nativeCandidateArtifactPhases{states: nativeBaseStateStub{state: base.state, artifact: base.artifact}, provenance: nativeBaseProvenanceStub{provenance: duplicate}, artifacts: base.store, storageDomain: "runtime", environment: "dev"}
	if _, err := service.nativeGenerationBase(t.Context(), &base.identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("duplicate binding error = %v", err)
	}
}

func TestNativeGenerationBaseRejectsDuplicateManagedDataPins(t *testing.T) {
	fixture := nativeInspectFixture(t)
	base := nativeBaseFixture(t, fixture)
	duplicate := base.provenance
	duplicate.Plan.ManagedDataPins = append(duplicate.Plan.ManagedDataPins, duplicate.Plan.ManagedDataPins[0])
	service := &nativeCandidateArtifactPhases{states: nativeBaseStateStub{state: base.state, artifact: base.artifact}, provenance: nativeBaseProvenanceStub{provenance: duplicate}, artifacts: base.store, storageDomain: "runtime", environment: "dev"}
	if _, err := service.nativeGenerationBase(t.Context(), &base.identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("duplicate managed-data pin error = %v", err)
	}
}

func testNativeDigest(value string) string {
	return testNativeDigestBytes([]byte(value))
}

func testNativeDigestBytes(value []byte) string {
	return "sha256:" + fmt.Sprintf("%x", sha256.Sum256(value))
}
