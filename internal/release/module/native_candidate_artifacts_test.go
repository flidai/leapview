package module

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	return object, err
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

func nativeInspectFixture(t *testing.T) nativeInspectFixtureValue {
	t.Helper()
	root := t.TempDir()
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
		"connections/warehouse.yaml": "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:warehouse, name: warehouse}\nspec: {type: managed}\n",
		"sources/orders.yaml":        "apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:orders, name: orders}\nspec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}\n",
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
