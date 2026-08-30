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

	"github.com/flidai/leapview/internal/extension"
	"github.com/flidai/leapview/internal/project"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

type nativeInspectReaderStub struct {
	artifact []byte
	refs     []project.CandidateSourceObjectRef
	objects  map[string][]byte
	errRefs  error
	errOpen  error
	opens    int
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
	request := release.CandidateArtifactRequest{CandidateID: "candidate-1", Scope: projectgraph.CandidateScope{ProjectID: "project:test", Environment: "dev"}, OwnerID: "owner-1", ArtifactDigest: sourceDigest, Source: project.CandidateSourceSnapshot{ProjectID: "project:test", ArtifactDigest: sourceDigest, ProjectFile: "leapview.yaml", ProjectDigest: projectDigest}}
	return nativeInspectFixtureValue{request: request, artifact: compiled.Canonical(), refs: refs, objects: objects}
}

func testNativeDigest(value string) string {
	return testNativeDigestBytes([]byte(value))
}

func testNativeDigestBytes(value []byte) string {
	return "sha256:" + fmt.Sprintf("%x", sha256.Sum256(value))
}
