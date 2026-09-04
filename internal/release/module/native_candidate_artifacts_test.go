package module

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	dashboardpublication "github.com/flidai/leapview/internal/dashboard/publication"
	"github.com/flidai/leapview/internal/extension"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	"github.com/flidai/leapview/internal/project"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/google/uuid"
)

type nativeInspectReaderStub struct {
	artifact []byte
	refs     []project.CandidateSourceObjectRef
	objects  map[string][]byte
	errRefs  error
	errOpen  error
	opens    int
}

type nativeBaseStateStub struct {
	state       servingstate.State
	artifact    servingstate.Artifact
	stateErr    error
	artifactErr error
}

func (r nativeBaseStateStub) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	if r.stateErr != nil {
		return servingstate.State{}, r.stateErr
	}
	return r.state, nil
}

func (r nativeBaseStateStub) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	if r.artifactErr != nil {
		return servingstate.Artifact{}, r.artifactErr
	}
	return r.artifact, nil
}

type nativeBaseProvenanceStub struct {
	provenance release.Provenance
	err        error
}

func (r nativeBaseProvenanceStub) ProvenanceForServingState(context.Context, projectgraph.ServingIdentity) (release.Provenance, error) {
	if r.err != nil {
		return release.Provenance{}, r.err
	}
	return r.provenance, nil
}

func (r *nativeInspectReaderStub) OpenProjectArtifact(context.Context, project.CandidateSourceScope, string) (io.ReadCloser, error) {
	if r.errOpen != nil {
		return nil, r.errOpen
	}
	return io.NopCloser(bytes.NewReader(r.artifact)), nil
}

func (r *nativeInspectReaderStub) SourceObjectRefs(context.Context, project.CandidateSourceScope, string) ([]project.CandidateSourceObjectRef, error) {
	if r.errRefs != nil {
		return nil, r.errRefs
	}
	return append([]project.CandidateSourceObjectRef(nil), r.refs...), nil
}

func (r *nativeInspectReaderStub) OpenSourceObject(_ context.Context, _ project.CandidateSourceScope, ref project.CandidateSourceObjectRef) (io.ReadCloser, error) {
	r.opens++
	if r.errOpen != nil {
		return nil, r.errOpen
	}
	body, ok := r.objects[ref.ObjectKey]
	if !ok {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

type nativeInspectExtensionStub struct{}

func (nativeInspectExtensionStub) PrepareExtensions(_ context.Context, names []string) ([]extension.Evidence, error) {
	result := make([]extension.Evidence, 0, len(names))
	for _, name := range names {
		digest := testNativeDigest(name)
		result = append(result, extension.Evidence{Name: name, Identity: digest, DuckDBVersion: "duckdb-test", ExtensionVersion: "v1", GOOS: "linux", GOARCH: "amd64", Platform: "linux_amd64", SupportProfile: "test", Digest: digest, Origin: "test", Provenance: "test", Signature: "test"})
	}
	return result, nil
}

type nativeInspectPinsStub struct{}

func (nativeInspectPinsStub) ValidateServingStatePins(context.Context, projectgraph.ServingIdentity, map[projectgraph.ResourceID]string) error {
	return nil
}

func (nativeInspectPinsStub) ResolveCandidatePins(_ context.Context, _ projectgraph.ResourceID, ids []projectgraph.ResourceID, _ string) (map[projectgraph.ResourceID]string, error) {
	result := make(map[projectgraph.ResourceID]string, len(ids))
	for _, id := range ids {
		result[id] = testNativeDigest(id.String())
	}
	return result, nil
}

func TestNativeCandidateInspectUsesObjectReaderAndReturnsCompilerArtifact(t *testing.T) {
	fixture := nativeInspectFixture(t)
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	request := fixture.request
	request.Source.ProjectPath = filepath.Join(t.TempDir(), "must-not-be-read")
	request.Source.ProjectArtifactPath = filepath.Join(t.TempDir(), "must-not-be-read")
	before := reader.opens
	set, err := service.InspectCandidateArtifacts(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if set.Compiler.Artifact.ProjectID() != request.Scope.ProjectID || set.Compiler.Artifact.Digest() != request.Source.ProjectDigest {
		t.Fatalf("compiler artifact identity = %s/%s", set.Compiler.Artifact.ProjectID(), set.Compiler.Artifact.Digest())
	}
	if set.Artifact.ContentDigest == "" || set.Artifact.ContentDigest != set.Generation.ArtifactDigest || set.Generation.ServingArtifactID != nativeServingArtifactID(set.Generation.ArtifactDigest) || set.Generation.BundleManifestJSON == "" {
		t.Fatalf("inspection did not bind deterministic serving identity: artifact=%#v generation=%#v", set.Artifact, set.Generation)
	}
	if set.Artifact.ContentDigest == request.ArtifactDigest {
		t.Fatalf("inspection reused source digest %q as serving bundle digest", request.ArtifactDigest)
	}
	if reader.opens-before != len(fixture.refs) {
		t.Fatalf("source object opens = %d, want %d", reader.opens-before, len(fixture.refs))
	}
	serialized, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), request.Source.ProjectPath) || strings.Contains(string(serialized), request.Source.ProjectArtifactPath) {
		t.Fatal("compiler evidence exposed caller filesystem paths")
	}
	for _, ref := range fixture.refs {
		if strings.Contains(string(serialized), ref.ObjectKey) {
			t.Fatalf("compiler evidence exposed opaque object key %q", ref.ObjectKey)
		}
	}
	for _, callerPath := range []string{request.Source.ProjectPath, request.Source.ProjectArtifactPath} {
		if _, statErr := os.Stat(callerPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("native inspect touched caller path %q: %v", callerPath, statErr)
		}
	}
}

func TestNativeCandidateInspectRejectsForgedArtifactIdentity(t *testing.T) {
	fixture := nativeInspectFixture(t)
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	request := fixture.request
	request.Source.ProjectDigest = testNativeDigest("forged")
	if _, err := service.InspectCandidateArtifacts(t.Context(), request); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("forged project digest error = %v", err)
	}
	request = fixture.request
	request.Scope.ProjectID = "project:other"
	if _, err := service.InspectCandidateArtifacts(t.Context(), request); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("mismatched project ID error = %v", err)
	}
}

func TestNativeCandidateInspectGenerationIDIsOptionalButValidatedWhenSupplied(t *testing.T) {
	fixture := nativeInspectFixture(t)
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}

	request := fixture.request
	request.GenerationID = ""
	if _, err := service.InspectCandidateArtifacts(t.Context(), request); err != nil {
		t.Fatalf("inspection without generation ID: %v", err)
	}
	request.GenerationID = uuid.NewString()
	if _, err := service.InspectCandidateArtifacts(t.Context(), request); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("inspection with non-v7 generation ID error = %v", err)
	}
}

func TestNativeCandidateInspectRejectsCorruptArtifact(t *testing.T) {
	fixture := nativeInspectFixture(t)
	reader := &nativeInspectReaderStub{artifact: []byte("not-an-artifact"), refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	if _, err := service.InspectCandidateArtifacts(t.Context(), fixture.request); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("corrupt artifact error = %v", err)
	}
}

func TestNativeCandidateInspectRejectsSourceTreeThatDoesNotMatchArtifact(t *testing.T) {
	fixture := nativeInspectFixture(t)
	for i := range fixture.refs {
		if fixture.refs[i].Path != "models/orders.yaml" {
			continue
		}
		body := bytes.ReplaceAll(fixture.objects[fixture.refs[i].ObjectKey], []byte("SELECT id FROM source.orders"), []byte("SELECT 1 AS id FROM source.orders"))
		fixture.objects[fixture.refs[i].ObjectKey] = body
		fixture.refs[i].Digest = testNativeDigestBytes(body)
		fixture.refs[i].SizeBytes = int64(len(body))
	}
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	if _, err := service.InspectCandidateArtifacts(t.Context(), fixture.request); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("mismatched source tree error = %v", err)
	}
}

func TestNativeCandidateInspectFailsClosedWhenBaseArtifactIsUnavailable(t *testing.T) {
	fixture := nativeInspectFixture(t)
	fixture.request.Scope.BaseGenerationID = "generation-active"
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	if _, err := service.InspectCandidateArtifacts(t.Context(), fixture.request); !errors.Is(err, release.ErrCandidateArtifactUnavailable) {
		t.Fatalf("missing base artifact error = %v", err)
	}
}

func TestNativeCandidateInspectClassifiesSourceReaderFailuresUnavailable(t *testing.T) {
	fixture := nativeInspectFixture(t)
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects, errRefs: errors.New("refs failed")}
	service := &nativeCandidateArtifactPhases{reader: reader, environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	if _, err := service.InspectCandidateArtifacts(t.Context(), fixture.request); !errors.Is(err, release.ErrCandidateArtifactUnavailable) {
		t.Fatalf("source refs error = %v", err)
	}
	reader.errRefs = nil
	reader.errOpen = errors.New("object failed")
	if _, err := service.InspectCandidateArtifacts(t.Context(), fixture.request); !errors.Is(err, release.ErrCandidateArtifactUnavailable) {
		t.Fatalf("source object error = %v", err)
	}
}

func TestNativeCandidateInspectDoesNotWriteServingOrObjectState(t *testing.T) {
	fixture := nativeInspectFixture(t)
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	if _, err := service.InspectCandidateArtifacts(t.Context(), fixture.request); err != nil {
		t.Fatal(err)
	}
	if reader.opens != len(fixture.refs) {
		t.Fatalf("reader open count = %d, want %d", reader.opens, len(fixture.refs))
	}
	if _, err := service.MaterializeCandidateArtifacts(t.Context(), fixture.request, release.CandidateArtifactSet{}); !errors.Is(err, release.ErrCandidateArtifactUnavailable) {
		t.Fatalf("materialize error = %v", err)
	}
	if _, err := service.HydrateCandidateArtifacts(t.Context(), fixture.request, release.CandidateArtifactSet{}, release.CandidateArtifactIdentity{}); !errors.Is(err, release.ErrCandidateArtifactUnavailable) {
		t.Fatalf("rehydrate error = %v", err)
	}
}

func TestNativeCandidateMaterializeAndHydrateUsesImmutableServingObject(t *testing.T) {
	fixture := nativeInspectFixture(t)
	store, err := platformobjectstore.NewMemoryStore(platformobjectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, artifacts: store, storageDomain: "runtime", environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	inspected, err := service.InspectCandidateArtifacts(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := service.MaterializeCandidateArtifacts(t.Context(), fixture.request, inspected)
	if err != nil {
		t.Fatal(err)
	}
	if materialized.Generation.Identity.GenerationID != fixture.request.GenerationID {
		t.Fatalf("generation id = %q, want caller-supplied %q", materialized.Generation.Identity.GenerationID, fixture.request.GenerationID)
	}
	parsedGenerationID, err := uuid.Parse(materialized.Generation.Identity.GenerationID)
	if err != nil || parsedGenerationID.Version() != 7 {
		t.Fatalf("generation id = %q is not UUIDv7: %v", materialized.Generation.Identity.GenerationID, err)
	}
	if materialized.Generation.Identity.GenerationID == materialized.Generation.ServingArtifactID {
		t.Fatal("serving state and artifact identities must remain distinct")
	}
	if materialized.Generation.ArtifactDigest == "" || materialized.Artifact.ContentDigest != materialized.Generation.ArtifactDigest {
		t.Fatalf("materialized artifact identity = %#v / %#v", materialized.Artifact, materialized.Generation)
	}
	object, err := store.Open(t.Context(), nativeServingArtifactKey(materialized.Generation.ArtifactDigest))
	if err != nil {
		t.Fatal(err)
	}
	validation, _, err := projectbundle.ValidateArtifactReader(object.Body, object.Info.SizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	object.Body.Close()
	if object.Info.ContentType != nativeServingArtifactContentType || object.Info.StorageSecurityDomain != "runtime" || object.Info.Digest != materialized.Generation.ArtifactDigest {
		t.Fatalf("serving object info = %#v", object.Info)
	}
	if materialized.Generation.BundleManifestJSON == "" || materialized.Generation.BundleManifestJSON != validation.ManifestJSON {
		t.Fatalf("materialized bundle manifest = %q, validated = %q", materialized.Generation.BundleManifestJSON, validation.ManifestJSON)
	}
	accessJSON, publicationsJSON, appearancesJSON, err := nativeServingDocuments(inspected.Compiler.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	if materialized.Generation.AccessPolicyJSON != accessJSON || materialized.Generation.DashboardPublicationsJSON != publicationsJSON || materialized.Generation.DashboardAppearancesJSON != appearancesJSON {
		t.Fatalf("materialized serving documents = %#v, want access=%q publications=%q appearances=%q", materialized.Generation, accessJSON, publicationsJSON, appearancesJSON)
	}
	if materialized.Generation.NativeArtifact.Locator != object.Info.Key || materialized.Generation.NativeArtifact.StorageSecurityDomain != object.Info.StorageSecurityDomain || materialized.Generation.NativeArtifact.ContentType != object.Info.ContentType || materialized.Generation.NativeArtifact.MetadataDigest != object.Info.MetadataDigest || materialized.Generation.NativeArtifact.SizeBytes != object.Info.SizeBytes {
		t.Fatalf("materialized native object evidence = %#v, object info = %#v", materialized.Generation.NativeArtifact, object.Info)
	}
	hydrated, err := service.HydrateCandidateArtifacts(t.Context(), fixture.request, inspected, release.CandidateArtifactIdentity{ServingArtifactID: materialized.Generation.ServingArtifactID, ServingArtifactDigest: materialized.Generation.ArtifactDigest, ServingStateID: materialized.Generation.Identity.GenerationID})
	if err != nil {
		t.Fatal(err)
	}
	if hydrated.Generation.Identity != materialized.Generation.Identity || hydrated.Compiler.Artifact.Digest() != inspected.Compiler.Artifact.Digest() || hydrated.Artifact.ContentDigest != materialized.Artifact.ContentDigest || hydrated.Generation.BundleManifestJSON != materialized.Generation.BundleManifestJSON || hydrated.Generation.NativeArtifact != materialized.Generation.NativeArtifact || hydrated.Generation.AccessPolicyJSON != materialized.Generation.AccessPolicyJSON || hydrated.Generation.DashboardPublicationsJSON != materialized.Generation.DashboardPublicationsJSON || hydrated.Generation.DashboardAppearancesJSON != materialized.Generation.DashboardAppearancesJSON {
		t.Fatalf("hydrated set drifted: %#v", hydrated.Generation)
	}
	replayed, err := service.MaterializeCandidateArtifacts(t.Context(), fixture.request, materialized)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Generation.BundleManifestJSON != materialized.Generation.BundleManifestJSON || replayed.Generation.NativeArtifact != materialized.Generation.NativeArtifact || replayed.Generation.AccessPolicyJSON != materialized.Generation.AccessPolicyJSON || replayed.Generation.DashboardPublicationsJSON != materialized.Generation.DashboardPublicationsJSON || replayed.Generation.DashboardAppearancesJSON != materialized.Generation.DashboardAppearancesJSON {
		t.Fatalf("materialized replay changed serving evidence: %#v != %#v", replayed.Generation, materialized.Generation)
	}
}

func TestNativeAuthorizationFingerprintIsGenerationIndependent(t *testing.T) {
	fixture := nativePolicyInspectFixture(t)
	fixture.request.CandidateID = "018f0e4e-6f2a-7abc-8def-0123456789aa"
	store, err := platformobjectstore.NewMemoryStore(platformobjectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, artifacts: store, storageDomain: "runtime", environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}

	inspected, err := service.InspectCandidateArtifacts(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspected.Generation.Restrictions) == 0 {
		t.Fatal("inspection did not retain data-policy restrictions")
	}

	for _, generationID := range []string{fixture.request.GenerationID, "018f0e4e-6f2a-7abd-8def-0123456789ab"} {
		request := fixture.request
		request.GenerationID = generationID
		materialized, err := service.MaterializeCandidateArtifacts(t.Context(), request, inspected)
		if err != nil {
			t.Fatalf("materialize generation %q: %v", generationID, err)
		}
		if materialized.AuthorizationFingerprint != inspected.AuthorizationFingerprint {
			t.Fatalf("materialized generation %q fingerprint = %q, want inspected %q", generationID, materialized.AuthorizationFingerprint, inspected.AuthorizationFingerprint)
		}

		recovered, err := service.RecoverCandidateArtifacts(t.Context(), release.CandidateArtifactRecoveryRequest{
			CandidateID: fixture.request.CandidateID, ServingIdentity: materialized.Generation.Identity, SourceDigest: request.ArtifactDigest,
			ManagedDataPins: materialized.Generation.ManagedDataPins,
			Artifact:        release.CandidateArtifactIdentity{ServingArtifactID: materialized.Generation.ServingArtifactID, ServingArtifactDigest: materialized.Generation.ArtifactDigest, ServingStateID: materialized.Generation.Identity.GenerationID},
		})
		if err != nil {
			t.Fatalf("recover generation %q: %v", generationID, err)
		}
		if recovered.AuthorizationFingerprint != inspected.AuthorizationFingerprint {
			t.Fatalf("recovered generation %q fingerprint = %q, want inspected %q", generationID, recovered.AuthorizationFingerprint, inspected.AuthorizationFingerprint)
		}
		if !reflect.DeepEqual(recovered.Generation.Restrictions, inspected.Generation.Restrictions) || !reflect.DeepEqual(recovered.Generation.Restrictions, materialized.Generation.Restrictions) {
			t.Fatalf("recovered generation %q restrictions = %#v, want %#v", generationID, recovered.Generation.Restrictions, inspected.Generation.Restrictions)
		}
	}
}

type nativeRecoveryFixtureValue struct {
	request release.CandidateArtifactRecoveryRequest
	store   *platformobjectstore.MemoryStore
	body    []byte
}

func nativeRecoveryFixture(t *testing.T) nativeRecoveryFixtureValue {
	return nativeRecoveryFixtureForConnector(t, "s3")
}

func nativeRecoveryFixtureForConnector(t *testing.T, connectorKind string) nativeRecoveryFixtureValue {
	t.Helper()
	fixture := nativeInspectFixture(t)
	files := make(map[string][]byte, len(fixture.refs))
	for _, ref := range fixture.refs {
		files[ref.Path] = fixture.objects[ref.ObjectKey]
	}
	files["connections/warehouse.yaml"] = []byte(fmt.Sprintf("apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:warehouse, name: warehouse}\nspec: {type: %s}\n", connectorKind))
	if connectorKind != "managed" {
		files["sources/orders.yaml"] = []byte("apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:orders, name: orders}\nspec: {connection: warehouse, location: {type: path, path: 's3://recovery/orders.csv', format: csv}}\n")
	}
	compiled, err := projectcompiler.CompileProjectFiles(files, fixture.request.Source.ProjectFile)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := projectcompiler.PlanProjectFilesAgainstGraph(files, fixture.request.Source.ProjectFile, compiled.Graph())
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	_, digest, err := projectbundle.PackCompiledProject(compiled, plan, &body)
	if err != nil {
		t.Fatal(err)
	}
	store, err := platformobjectstore.NewMemoryStore(platformobjectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	metadata := platformobjectstore.ObjectMetadata{StorageSecurityDomain: "runtime", Digest: digest, SizeBytes: int64(body.Len()), ContentType: nativeServingArtifactContentType, MetadataDigest: nativeServingArtifactMetadataDigest()}
	if _, err := store.PutImmutable(t.Context(), nativeServingArtifactKey(digest), bytes.NewReader(body.Bytes()), metadata); err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(fixture.request.Scope.ProjectID, fixture.request.Scope.Environment, fixture.request.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	return nativeRecoveryFixtureValue{
		request: release.CandidateArtifactRecoveryRequest{
			CandidateID: "018f0e4e-6f2a-7abc-8def-0123456789aa", ServingIdentity: identity, SourceDigest: fixture.request.ArtifactDigest,
			Artifact: release.CandidateArtifactIdentity{ServingArtifactID: nativeServingArtifactID(digest), ServingArtifactDigest: digest, ServingStateID: identity.GenerationID},
		},
		store: store, body: body.Bytes(),
	}
}

func TestNativeCandidateRecoverUsesImmutableBundleWithoutSourceReader(t *testing.T) {
	fixture := nativeRecoveryFixture(t)
	store := &nativeForgedArtifactStore{ImmutableStore: fixture.store}
	service := &nativeCandidateArtifactPhases{artifacts: store, storageDomain: "runtime", environment: "dev", extensionPreparation: nativeInspectExtensionStub{}}
	set, err := service.RecoverCandidateArtifacts(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if set.Artifact.SourceDigest != fixture.request.SourceDigest || set.Artifact.ContentDigest != fixture.request.Artifact.ServingArtifactDigest || set.Artifact.ProjectDigest != set.Compiler.Artifact.Digest() || set.Artifact.CompilerVersion != projectartifact.CompilerVersion || set.Artifact.SchemaVersion != projectartifact.Version {
		t.Fatalf("recovered artifact provenance = %#v", set.Artifact)
	}
	if set.Generation.Identity != fixture.request.ServingIdentity || set.Generation.ServingArtifactID != fixture.request.Artifact.ServingArtifactID || set.Generation.ArtifactDigest != fixture.request.Artifact.ServingArtifactDigest || set.Generation.DataMode != release.GenerationDataRefreshSources {
		t.Fatalf("recovered generation = %#v", set.Generation)
	}
	if set.Generation.NativeArtifact.Locator != nativeServingArtifactKey(fixture.request.Artifact.ServingArtifactDigest) || set.Generation.NativeArtifact.SizeBytes != int64(len(fixture.body)) || set.Generation.BundleManifestJSON == "" || set.Generation.AccessPolicyJSON == "" || set.Generation.DashboardPublicationsJSON == "" || set.Generation.DashboardAppearancesJSON == "" {
		t.Fatalf("recovered serving evidence = %#v", set.Generation)
	}
	if len(set.Extensions) != 2 || set.Extensions[0].Name != "ducklake" || set.Extensions[1].Name != "httpfs" {
		t.Fatalf("recovered extension evidence = %#v, want exact bundle requirements", set.Extensions)
	}
	if set.Compiler.Graph.Validate() != nil || set.Compiler.Graph.ProjectID() != fixture.request.ServingIdentity.ProjectID || set.Compiler.Manifest.ID != fixture.request.ServingIdentity.ProjectID.String() || set.Compiler.Plan.Project != fixture.request.ServingIdentity.ProjectID.String() {
		t.Fatalf("recovered compiler evidence = %#v", set.Compiler)
	}
	if store.opens != 1 || store.puts != 0 {
		t.Fatalf("recovery object calls = opens %d puts %d, want one read and no writes", store.opens, store.puts)
	}
	service.extensionPreparation = nil
	if _, err := service.RecoverCandidateArtifacts(t.Context(), fixture.request); !errors.Is(err, release.ErrCandidateArtifactUnavailable) {
		t.Fatalf("missing extension admission error = %v, want unavailable", err)
	}
}

func TestNativeCandidateRecoverRejectsManagedBundleWithoutPins(t *testing.T) {
	fixture := nativeRecoveryFixtureForConnector(t, "managed")
	service := &nativeCandidateArtifactPhases{artifacts: fixture.store, storageDomain: "runtime", environment: "dev", extensionPreparation: nativeInspectExtensionStub{}}
	if _, err := service.RecoverCandidateArtifacts(t.Context(), fixture.request); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("managed recovery error = %v, want invalid", err)
	}
}

func TestNativeCandidateRecoverRejectsMissingBodyAndForgedMetadata(t *testing.T) {
	fixture := nativeRecoveryFixture(t)
	service := &nativeCandidateArtifactPhases{artifacts: &nativeForgedArtifactStore{ImmutableStore: fixture.store, nilBody: true}, storageDomain: "runtime", environment: "dev", extensionPreparation: nativeInspectExtensionStub{}}
	if _, err := service.RecoverCandidateArtifacts(t.Context(), fixture.request); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("nil body error = %v", err)
	}
	for name, mutate := range map[string]func(*platformobjectstore.ObjectInfo){
		"domain":          func(info *platformobjectstore.ObjectInfo) { info.StorageSecurityDomain = "other" },
		"digest":          func(info *platformobjectstore.ObjectInfo) { info.Digest = testNativeDigest("other") },
		"size":            func(info *platformobjectstore.ObjectInfo) { info.SizeBytes++ },
		"content type":    func(info *platformobjectstore.ObjectInfo) { info.ContentType = "application/octet-stream" },
		"metadata digest": func(info *platformobjectstore.ObjectInfo) { info.MetadataDigest = testNativeDigest("other") },
		"key":             func(info *platformobjectstore.ObjectInfo) { info.Key = "serving-artifacts/other.tar.gz" },
	} {
		t.Run(name, func(t *testing.T) {
			service.artifacts = &nativeForgedArtifactStore{ImmutableStore: fixture.store, mutate: mutate}
			if _, err := service.RecoverCandidateArtifacts(t.Context(), fixture.request); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
				t.Fatalf("forged metadata error = %v", err)
			}
		})
	}
}

func TestNativeCandidateRecoverRejectsIdentityAndBundleMismatches(t *testing.T) {
	fixture := nativeRecoveryFixture(t)
	service := &nativeCandidateArtifactPhases{artifacts: fixture.store, storageDomain: "runtime", environment: "dev", extensionPreparation: nativeInspectExtensionStub{}}
	for name, mutate := range map[string]func(*release.CandidateArtifactRecoveryRequest){
		"candidate": func(request *release.CandidateArtifactRecoveryRequest) { request.CandidateID = " candidate" },
		"serving project": func(request *release.CandidateArtifactRecoveryRequest) {
			request.ServingIdentity.ProjectID = "project:other"
		},
		"serving environment": func(request *release.CandidateArtifactRecoveryRequest) { request.ServingIdentity.Environment = "prod" },
		"serving generation": func(request *release.CandidateArtifactRecoveryRequest) {
			request.ServingIdentity.GenerationID = uuid.NewString()
		},
		"artifact digest": func(request *release.CandidateArtifactRecoveryRequest) {
			request.Artifact.ServingArtifactDigest = testNativeDigest("other")
		},
		"artifact state": func(request *release.CandidateArtifactRecoveryRequest) {
			request.Artifact.ServingStateID = "018f0e4e-6f2a-7abc-8def-0123456789ac"
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := fixture.request
			mutate(&request)
			if _, err := service.RecoverCandidateArtifacts(t.Context(), request); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
				t.Fatalf("identity error = %v", err)
			}
		})
	}
	service.artifacts = &nativeForgedArtifactStore{ImmutableStore: fixture.store, body: []byte("not-a-bundle")}
	if _, err := service.RecoverCandidateArtifacts(t.Context(), fixture.request); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("corrupt bundle error = %v", err)
	}
}

func TestNativeCandidateMaterializeAndHydrateRequireExactGenerationID(t *testing.T) {
	fixture := nativeInspectFixture(t)
	store, err := platformobjectstore.NewMemoryStore(platformobjectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, artifacts: store, storageDomain: "runtime", environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	inspected, err := service.InspectCandidateArtifacts(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := service.MaterializeCandidateArtifacts(t.Context(), fixture.request, inspected)
	if err != nil {
		t.Fatal(err)
	}
	otherGenerationID := "018f0e4e-6f2a-7abd-8def-0123456789ab"
	request := fixture.request
	request.GenerationID = otherGenerationID
	if _, err := service.MaterializeCandidateArtifacts(t.Context(), request, materialized); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("materialize generation mismatch error = %v", err)
	}
	identity := release.CandidateArtifactIdentity{ServingArtifactID: materialized.Generation.ServingArtifactID, ServingArtifactDigest: materialized.Generation.ArtifactDigest, ServingStateID: materialized.Generation.Identity.GenerationID}
	if _, err := service.HydrateCandidateArtifacts(t.Context(), request, materialized, identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("hydrate generation mismatch error = %v", err)
	}
	request.GenerationID = ""
	if _, err := service.HydrateCandidateArtifacts(t.Context(), request, materialized, identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("hydrate missing generation ID error = %v", err)
	}
	if _, err := service.MaterializeCandidateArtifacts(t.Context(), request, inspected); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("materialize missing generation ID error = %v", err)
	}
}

func TestNativeCandidateServingDocumentsAreCanonicalAndBounded(t *testing.T) {
	fixture := nativeInspectFixture(t)
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	inspected, err := service.InspectCandidateArtifacts(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Generation.AccessPolicyJSON != "{}" || inspected.Generation.DashboardPublicationsJSON != "{}" || inspected.Generation.DashboardAppearancesJSON != "{}" {
		t.Fatalf("empty manifest serving documents = %#v, want canonical empty objects", inspected.Generation)
	}
	if err := validateNativeServingDocument(` {}`, "test"); err == nil {
		t.Fatal("non-canonical serving document was accepted")
	}
	if err := validateNativeServingDocument(`{"z":1,"a":2}`, "test"); err == nil {
		t.Fatal("non-canonical serving document key order was accepted")
	}
	if err := validateNativeServingDocument(`{"x":1,"x":2}`, "test"); err == nil {
		t.Fatal("duplicate serving document key was accepted")
	}
	oversized := `{"x":"` + strings.Repeat("a", int(maxNativeServingDocumentBytes)) + `"}`
	if err := validateNativeServingDocument(oversized, "test"); err == nil {
		t.Fatal("oversized serving document was accepted")
	}
}

func TestNativeCandidateServingDocumentsCanonicalizeNonEmptyManifestObjects(t *testing.T) {
	manifest := projectmanifest.Project{
		Access: projectmanifest.AccessPolicy{Grants: map[string]projectmanifest.Grant{
			"grant": {ID: "grant", Name: "Grant", Object: projectmanifest.SecurableRef{Kind: "dashboard", ID: "dashboard:test"}, Subject: projectmanifest.Subject{Kind: "principal", PrincipalID: "alice"}, Capability: "read"},
		}},
		Publications: map[string]dashboardpublication.Definition{
			"public": {Name: "public", Dashboard: "dashboard:test", DefaultPage: "overview", DependencyAssetIDs: []string{"dashboard:test"}, ConfigurationDigest: testNativeDigest("publication")},
		},
	}
	access, publications, appearances, err := nativeServingDocumentsFromManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if access == "{}" || publications == "{}" || appearances != "{}" {
		t.Fatalf("canonical serving documents = access=%q publications=%q appearances=%q", access, publications, appearances)
	}
	for label, value := range map[string]string{"access": access, "publications": publications, "appearances": appearances} {
		if err := validateNativeServingDocument(value, label); err != nil {
			t.Fatalf("%s document is not canonical: %v", label, err)
		}
	}
	if !strings.HasPrefix(access, `{"grants":`) || !strings.HasPrefix(publications, `{"public":`) {
		t.Fatalf("serving documents are not deterministically key ordered: access=%q publications=%q", access, publications)
	}
}

func TestNativeCandidateMaterializeRejectsTamperedBundleManifestEvidence(t *testing.T) {
	fixture := nativeInspectFixture(t)
	store, err := platformobjectstore.NewMemoryStore(platformobjectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, artifacts: store, storageDomain: "runtime", environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	inspected, err := service.InspectCandidateArtifacts(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := service.MaterializeCandidateArtifacts(t.Context(), fixture.request, inspected)
	if err != nil {
		t.Fatal(err)
	}
	materialized.Generation.BundleManifestJSON = `{"tampered":true}`
	if _, err := service.MaterializeCandidateArtifacts(t.Context(), fixture.request, materialized); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("tampered materialize manifest error = %v", err)
	}
	identity := release.CandidateArtifactIdentity{ServingArtifactID: materialized.Generation.ServingArtifactID, ServingArtifactDigest: materialized.Generation.ArtifactDigest, ServingStateID: materialized.Generation.Identity.GenerationID}
	if _, err := service.HydrateCandidateArtifacts(t.Context(), fixture.request, materialized, identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("tampered hydrate manifest error = %v", err)
	}
}

func TestNativeCandidateMaterializeReplaysContentAddressedObjectAndLostAck(t *testing.T) {
	fixture := nativeInspectFixture(t)
	store, err := platformobjectstore.NewMemoryStore(platformobjectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, artifacts: store, storageDomain: "runtime", environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	inspected, err := service.InspectCandidateArtifacts(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	store.SimulateLostCommitAcknowledgement()
	first, err := service.MaterializeCandidateArtifacts(t.Context(), fixture.request, inspected)
	if err != nil {
		t.Fatalf("lost acknowledgement was not reconciled: %v", err)
	}
	second, err := service.MaterializeCandidateArtifacts(t.Context(), fixture.request, inspected)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation.Identity != second.Generation.Identity || first.Generation.ServingArtifactID != second.Generation.ServingArtifactID || first.Generation.ArtifactDigest != second.Generation.ArtifactDigest {
		t.Fatalf("content-addressed replay changed identity: %#v != %#v", first.Generation, second.Generation)
	}
	objects, _, err := store.List(t.Context(), strings.TrimSuffix(nativeServingArtifactPrefix, "/"), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 {
		t.Fatalf("serving object count = %d, want one immutable object", len(objects))
	}
}

func TestNativeCandidateMaterializeRestoresAuthoredRequirementsForEffectiveRefresh(t *testing.T) {
	fixture := nativeInspectFixtureForConnector(t, "http")
	store, err := platformobjectstore.NewMemoryStore(platformobjectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, artifacts: store, storageDomain: "runtime", environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	inspected, err := service.InspectCandidateArtifacts(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]release.CandidateAuthoredConnection(nil), inspected.Generation.AuthoredConnections...)
	if len(want) == 0 {
		t.Fatal("fixture did not produce authored connection requirements")
	}
	// EffectiveCandidateArtifacts may convert an unchanged base projection to
	// refresh_sources after inspection. Reproduce its intentionally empty
	// authored list and require materialization to restore the immutable set.
	inspected.Generation.AuthoredConnections = nil
	materialized, err := service.MaterializeCandidateArtifacts(t.Context(), fixture.request, inspected)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(materialized.Generation.AuthoredConnections, want) {
		t.Fatalf("materialized authored requirements = %#v, want %#v", materialized.Generation.AuthoredConnections, want)
	}
}

func TestNativeCandidateMaterializeRejectsAmbiguousReplayContentIdentityMismatch(t *testing.T) {
	fixture := nativeInspectFixture(t)
	baseStore, err := platformobjectstore.NewMemoryStore(platformobjectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, artifacts: baseStore, storageDomain: "runtime", environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	inspected, err := service.InspectCandidateArtifacts(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := service.MaterializeCandidateArtifacts(t.Context(), fixture.request, inspected)
	if err != nil {
		t.Fatal(err)
	}
	object, err := baseStore.Open(t.Context(), nativeServingArtifactKey(materialized.Generation.ArtifactDigest))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(object.Body)
	_ = object.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	metadata := platformobjectstore.ObjectMetadata{StorageSecurityDomain: "runtime", SizeBytes: int64(len(body)), ContentType: nativeServingArtifactContentType, MetadataDigest: materialized.Generation.NativeArtifact.MetadataDigest}
	cases := []struct {
		name, digest, projectID string
	}{
		{name: "digest", digest: testNativeDigest("different-bundle"), projectID: materialized.Compiler.Artifact.ProjectID().String()},
		{name: "project", digest: materialized.Generation.ArtifactDigest, projectID: "project:other"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			metadata.Digest = testCase.digest
			store := &nativeAmbiguousReplayStore{ImmutableStore: baseStore, body: body}
			service.artifacts = store
			if _, err := service.putServingArtifact(t.Context(), nativeServingArtifactKey(testCase.digest), bytes.NewReader(body), metadata, testCase.projectID); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
				t.Fatalf("ambiguous replay %s mismatch error = %v", testCase.name, err)
			}
		})
	}
}

func TestNativeCandidateHydrateRejectsForgedObjectMetadata(t *testing.T) {
	fixture := nativeInspectFixture(t)
	baseStore, err := platformobjectstore.NewMemoryStore(platformobjectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	reader := &nativeInspectReaderStub{artifact: fixture.artifact, refs: fixture.refs, objects: fixture.objects}
	service := &nativeCandidateArtifactPhases{reader: reader, artifacts: baseStore, storageDomain: "runtime", environment: "dev", pins: nativeInspectPinsStub{}, extensionPreparation: nativeInspectExtensionStub{}}
	inspected, err := service.InspectCandidateArtifacts(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := service.MaterializeCandidateArtifacts(t.Context(), fixture.request, inspected)
	if err != nil {
		t.Fatal(err)
	}
	identity := release.CandidateArtifactIdentity{ServingArtifactID: materialized.Generation.ServingArtifactID, ServingArtifactDigest: materialized.Generation.ArtifactDigest, ServingStateID: materialized.Generation.Identity.GenerationID}
	for name, mutate := range map[string]func(*platformobjectstore.ObjectInfo){
		"domain":   func(info *platformobjectstore.ObjectInfo) { info.StorageSecurityDomain = "forged" },
		"digest":   func(info *platformobjectstore.ObjectInfo) { info.Digest = testNativeDigest("forged") },
		"size":     func(info *platformobjectstore.ObjectInfo) { info.SizeBytes++ },
		"type":     func(info *platformobjectstore.ObjectInfo) { info.ContentType = "application/octet-stream" },
		"metadata": func(info *platformobjectstore.ObjectInfo) { info.MetadataDigest = testNativeDigest("forged") },
		"key":      func(info *platformobjectstore.ObjectInfo) { info.Key = "serving-artifacts/forged.tar.gz" },
	} {
		wrapped := &nativeForgedArtifactStore{ImmutableStore: baseStore, mutate: mutate}
		service.artifacts = wrapped
		if _, err := service.HydrateCandidateArtifacts(t.Context(), fixture.request, inspected, identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
			t.Fatalf("forged %s metadata error = %v", name, err)
		}
	}
	service.artifacts = &nativeForgedArtifactStore{ImmutableStore: baseStore, body: []byte("corrupt bundle")}
	if _, err := service.HydrateCandidateArtifacts(t.Context(), fixture.request, inspected, identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("corrupt serving object error = %v", err)
	}
	service.artifacts = &nativeForgedArtifactStore{ImmutableStore: baseStore, openErr: errors.New("object store unavailable")}
	if _, err := service.HydrateCandidateArtifacts(t.Context(), fixture.request, inspected, identity); !errors.Is(err, release.ErrCandidateArtifactUnavailable) {
		t.Fatalf("unavailable serving object error = %v", err)
	}
	tampered := materialized
	tampered.Generation.NativeArtifact.MetadataDigest = testNativeDigest("forged")
	service.artifacts = baseStore
	if _, err := service.HydrateCandidateArtifacts(t.Context(), fixture.request, tampered, identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("tampered retained object evidence error = %v", err)
	}
	missingEvidence := materialized
	missingEvidence.Generation.NativeArtifact = release.NativeArtifactObjectEvidence{}
	if _, err := service.HydrateCandidateArtifacts(t.Context(), fixture.request, missingEvidence, identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("missing retained object evidence error = %v", err)
	}
	if _, err := service.MaterializeCandidateArtifacts(t.Context(), fixture.request, missingEvidence); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("missing replay object evidence error = %v", err)
	}
	identity.ServingStateID = "not-a-uuid"
	service.artifacts = baseStore
	if _, err := service.HydrateCandidateArtifacts(t.Context(), fixture.request, inspected, identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("invalid serving state identity error = %v", err)
	}
	identity.ServingStateID = uuid.NewString()
	if _, err := service.HydrateCandidateArtifacts(t.Context(), fixture.request, inspected, identity); !errors.Is(err, release.ErrCandidateArtifactInvalid) {
		t.Fatalf("foreign serving state identity error = %v", err)
	}
}

type nativeForgedArtifactStore struct {
	platformobjectstore.ImmutableStore
	mutate  func(*platformobjectstore.ObjectInfo)
	openErr error
	body    []byte
	nilBody bool
	opens   int
	puts    int
}

type nativeAmbiguousReplayStore struct {
	platformobjectstore.ImmutableStore
	body []byte
	info platformobjectstore.ObjectInfo
}

func (s *nativeAmbiguousReplayStore) PutImmutable(_ context.Context, key string, _ io.Reader, metadata platformobjectstore.ObjectMetadata) (platformobjectstore.ObjectInfo, error) {
	s.info = platformobjectstore.ObjectInfo{Key: key, StorageSecurityDomain: metadata.StorageSecurityDomain, Digest: metadata.Digest, SizeBytes: metadata.SizeBytes, ContentType: metadata.ContentType, MetadataDigest: metadata.MetadataDigest}
	return platformobjectstore.ObjectInfo{}, platformobjectstore.ErrAmbiguous
}

func (s *nativeAmbiguousReplayStore) Open(_ context.Context, _ string) (platformobjectstore.Object, error) {
	return platformobjectstore.Object{Body: io.NopCloser(bytes.NewReader(s.body)), Info: s.info}, nil
}

func (s *nativeForgedArtifactStore) Open(ctx context.Context, key string) (platformobjectstore.Object, error) {
	s.opens++
	if s.openErr != nil {
		return platformobjectstore.Object{}, s.openErr
	}
	object, err := s.ImmutableStore.Open(ctx, key)
	if err == nil && s.mutate != nil {
		s.mutate(&object.Info)
	}
	if err == nil && s.body != nil {
		_ = object.Body.Close()
		object.Body = io.NopCloser(bytes.NewReader(s.body))
	}
	if err == nil && s.nilBody {
		_ = object.Body.Close()
		object.Body = nil
	}
	return object, err
}

func (s *nativeForgedArtifactStore) PutImmutable(ctx context.Context, key string, body io.Reader, metadata platformobjectstore.ObjectMetadata) (platformobjectstore.ObjectInfo, error) {
	s.puts++
	return s.ImmutableStore.PutImmutable(ctx, key, body, metadata)
}

func TestReadNativeInspectBodyEnforcesEmptyObjectSize(t *testing.T) {
	if _, err := readNativeInspectBody(io.NopCloser(strings.NewReader("x")), 8, 0); !errors.Is(err, errNativeInspectSize) {
		t.Fatal("non-empty body with zero expected size was accepted")
	}
	if body, err := readNativeInspectBody(io.NopCloser(strings.NewReader("")), 8, 0); err != nil || len(body) != 0 {
		t.Fatalf("empty body = %q, err=%v", body, err)
	}
}

type nativeInspectFixtureValue struct {
	request  release.CandidateArtifactRequest
	artifact []byte
	refs     []project.CandidateSourceObjectRef
	objects  map[string][]byte
}

func nativePolicyInspectFixture(t *testing.T) nativeInspectFixtureValue {
	t.Helper()
	fixture := nativeInspectFixture(t)
	var projectRef *project.CandidateSourceObjectRef
	for index := range fixture.refs {
		if fixture.refs[index].Path == fixture.request.Source.ProjectFile {
			projectRef = &fixture.refs[index]
			break
		}
	}
	if projectRef == nil {
		t.Fatal("native inspect fixture is missing project file")
	}
	projectYAML := strings.Replace(string(fixture.objects[projectRef.ObjectKey]), "access: {include: []}", "access: {include: [access/*.yaml]}", 1)
	if projectYAML == string(fixture.objects[projectRef.ObjectKey]) {
		t.Fatal("native inspect fixture project file did not contain empty access include")
	}
	fixture.objects[projectRef.ObjectKey] = []byte(projectYAML)
	projectRef.Digest = testNativeDigestBytes([]byte(projectYAML))
	projectRef.SizeBytes = int64(len(projectYAML))
	policyYAML := []byte("apiVersion: leapview.dev/v1\nkind: DataPolicy\nmetadata: {id: policy:orders, name: orders}\nspec: {object: {kind: model, id: model:orders}, subject: {kind: principal, principalId: principal:alice}, policyType: row_filter, expression: {field: id, operator: equals, value: '1'}}\n")
	const policyKey = "source_access_orders.yaml"
	fixture.objects[policyKey] = policyYAML
	fixture.refs = append(fixture.refs, project.CandidateSourceObjectRef{Path: "access/orders.yaml", Digest: testNativeDigestBytes(policyYAML), SizeBytes: int64(len(policyYAML)), ObjectKey: policyKey, ContentType: "text/plain", MetadataDigest: testNativeDigest("access/orders.yaml"), StorageSecurityDomain: "runtime"})

	files := make(map[string][]byte, len(fixture.refs))
	for _, ref := range fixture.refs {
		files[ref.Path] = fixture.objects[ref.ObjectKey]
	}
	compiled, err := projectcompiler.CompileProjectFiles(files, fixture.request.Source.ProjectFile)
	if err != nil {
		t.Fatal(err)
	}
	fixture.artifact = compiled.Canonical()
	fixture.request.Source.ProjectDigest = compiled.Digest()
	return fixture
}

func nativeInspectFixture(t *testing.T) nativeInspectFixtureValue {
	return nativeInspectFixtureForConnector(t, "managed")
}

func nativeInspectFixtureForConnector(t *testing.T, connectorKind string) nativeInspectFixtureValue {
	t.Helper()
	root := t.TempDir()
	sourcePath := "orders.csv"
	if connectorKind == "http" {
		sourcePath = "https://example.com/native-inspect/orders.csv"
	}
	files := map[string]string{
		"leapview.yaml": `apiVersion: leapview.dev/v1
kind: Project
metadata: {id: project:test, name: test}
spec:
  connections: {include: [connections/*.yaml]}
  sources: {include: [sources/*.yaml]}
  models: {include: [models/*.yaml]}
  semanticModels: {include: []}
  pipelines: {include: []}
  dashboards: {include: []}
  access: {include: []}
  publications: {include: []}
`,
		"connections/warehouse.yaml": fmt.Sprintf("apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:warehouse, name: warehouse}\nspec: {type: %s}\n", connectorKind),
		"sources/orders.yaml":        fmt.Sprintf("apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:orders, name: orders}\nspec: {connection: warehouse, location: {type: path, path: %s, format: csv}}\n", sourcePath),
		"models/orders.yaml":         "apiVersion: leapview.dev/v1\nkind: Model\nmetadata: {id: model:orders, name: orders_model}\nspec: {definition: {type: sql, sql: 'SELECT id FROM source.orders'}, fields: {id: {datatype: Integer}}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}}\n",
	}
	projectPath := filepath.Join(root, "leapview.yaml")
	for name, body := range files {
		filePath := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filePath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	compiled, err := projectcompiler.CompileProject(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	refs := make([]project.CandidateSourceObjectRef, 0, len(files))
	objects := make(map[string][]byte, len(files))
	for name, body := range files {
		data := []byte(body)
		digest := testNativeDigestBytes(data)
		key := "source/" + strings.ReplaceAll(name, "/", "_")
		refs = append(refs, project.CandidateSourceObjectRef{Path: name, Digest: digest, SizeBytes: int64(len(data)), ObjectKey: key, ContentType: "text/plain", MetadataDigest: testNativeDigest(name), StorageSecurityDomain: "runtime"})
		objects[key] = data
	}
	// Map iteration is intentionally arbitrary; only exact paths and refs are
	// relevant to the object-backed reader contract.
	projectDigest := compiled.Digest()
	sourceDigest := testNativeDigest("source-snapshot")
	request := release.CandidateArtifactRequest{CandidateID: "candidate-1", GenerationID: "018f0e4e-6f2a-7abc-8def-0123456789ab", Scope: projectgraph.CandidateScope{ProjectID: "project:test", Environment: "dev"}, OwnerID: "owner-1", ArtifactDigest: sourceDigest, Source: project.CandidateSourceSnapshot{ProjectID: "project:test", ArtifactDigest: sourceDigest, ProjectFile: "leapview.yaml", ProjectDigest: projectDigest}}
	return nativeInspectFixtureValue{request: request, artifact: compiled.Canonical(), refs: refs, objects: objects}
}

type nativeBaseFixtureValue struct {
	identity   projectgraph.ServingIdentity
	state      servingstate.State
	artifact   servingstate.Artifact
	provenance release.Provenance
	store      *platformobjectstore.MemoryStore
}

func nativeBaseFixture(t *testing.T, fixture nativeInspectFixtureValue) nativeBaseFixtureValue {
	t.Helper()
	compiled, err := projectartifact.Decode(fixture.artifact)
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string][]byte, len(fixture.refs))
	for _, ref := range fixture.refs {
		files[ref.Path] = fixture.objects[ref.ObjectKey]
	}
	plan, err := projectcompiler.PlanProjectFilesAgainstGraph(files, fixture.request.Source.ProjectFile, compiled.Graph())
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	bundleManifest, digest, err := projectbundle.PackCompiledProject(compiled, plan, &body)
	if err != nil {
		t.Fatal(err)
	}
	manifestJSON, err := nativeBundleManifestJSON(bundleManifest)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(fixture.request.Scope.ProjectID, fixture.request.Scope.Environment, fixture.request.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	metadataDigest := testNativeDigest("base-object-metadata")
	store, err := platformobjectstore.NewMemoryStore(platformobjectstore.MemoryStoreConfig{StorageSecurityDomain: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PutImmutable(t.Context(), nativeServingArtifactKey(digest), bytes.NewReader(body.Bytes()), platformobjectstore.ObjectMetadata{StorageSecurityDomain: "runtime", Digest: digest, SizeBytes: int64(body.Len()), ContentType: nativeServingArtifactContentType, MetadataDigest: metadataDigest})
	if err != nil {
		t.Fatal(err)
	}
	accessJSON, publicationsJSON, appearancesJSON, err := nativeServingDocuments(compiled)
	if err != nil {
		t.Fatal(err)
	}
	state := servingstate.State{ID: servingstate.ID(identity.GenerationID), ProjectID: identity.ProjectID, ProjectDigest: compiled.Digest(), AccessPolicyJSON: accessJSON, DashboardPublicationsJSON: publicationsJSON, DashboardAppearancesJSON: appearancesJSON, Environment: servingstate.Environment(identity.Environment), Status: servingstate.StatusActive, Digest: digest, ManifestJSON: manifestJSON, DuckLakeSnapshotID: 17}
	artifact := servingstate.Artifact{ID: nativeServingArtifactID(digest), ServingStateID: state.ID, Digest: digest, Format: servingstate.ArtifactBundleFormat, Locator: nativeServingArtifactKey(digest), StorageSecurityDomain: "runtime", ContentType: nativeServingArtifactContentType, MetadataDigest: metadataDigest, ManifestJSON: manifestJSON, SizeBytes: int64(body.Len())}
	artifactProvenance := release.ProjectArtifactProvenance{SourceDigest: fixture.request.ArtifactDigest, ProjectDigest: compiled.Digest(), ContentDigest: digest, CompilerVersion: projectartifact.CompilerVersion, SchemaVersion: projectartifact.Version}
	planProvenance := release.GenerationPlanProvenance{Identity: identity, TargetID: "target-dev", RuntimeVersion: "runtime:test", PolicyDigest: testNativeDigest("base-policy"), DataRevision: "snapshot:17", DataMode: release.GenerationDataReuseBase, ManagedDataPins: []release.ManagedDataPin{{ConnectionID: "connection:warehouse", RevisionID: "revision:base"}}}
	gate, err := (release.GateEvidence{Version: 1, CandidateID: "base-candidate", SourceDigest: artifactProvenance.SourceDigest, BindingGeneration: release.BindingFingerprint(nil), RuntimeVersion: planProvenance.RuntimeVersion, DuckDBVersion: "duckdb:test", Outcome: release.GateSuccess, EvaluatedAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC), Bounds: release.GateBounds{MaxRows: 10, MaxQueries: 1, MaxMillis: 100}}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	planProvenance.GateEvidence = &gate
	provenance, err := release.NewProvenance(release.ProvenanceInput{Artifact: artifactProvenance, Candidate: release.CandidateProvenance{ID: "base-candidate", Revision: 1, OwnerID: "base-owner"}, Plan: planProvenance})
	if err != nil {
		t.Fatal(err)
	}
	return nativeBaseFixtureValue{identity: identity, state: state, artifact: artifact, provenance: provenance, store: store}
}
