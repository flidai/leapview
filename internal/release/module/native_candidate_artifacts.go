package module

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/extension"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	"github.com/flidai/leapview/internal/project"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/google/uuid"
)

// nativeCandidateArtifactPhases keeps source evidence and serving artifact
// storage behind neutral ports. It never opens a database or a local
// filesystem: materialization is one immutable object write and hydration is
// one exact object read.
type nativeCandidateArtifactPhases struct {
	reader               project.CandidateSourceObjectReader
	artifacts            platformobjectstore.ImmutableStore
	storageDomain        string
	environment          servingstate.Environment
	pins                 ManagedDataPins
	extensionPreparation extension.Preparation
}

var _ candidateArtifactPhases = (*nativeCandidateArtifactPhases)(nil)

const (
	maxNativeInspectArtifactBytes    int64 = 64 << 20
	maxNativeInspectSourceBytes      int64 = 64 << 20
	maxNativeInspectSourceFiles            = 10_000
	nativeServingArtifactContentType       = projectbundle.BundleContentType
	nativeServingArtifactPrefix            = "serving-artifacts/"
	nativeServingArtifactSuffix            = ".tar.gz"
)

func (service *nativeCandidateArtifactPhases) InspectCandidateArtifacts(ctx context.Context, request release.CandidateArtifactRequest) (release.CandidateArtifactSet, error) {
	if service == nil || service.reader == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	if err := validateNativeInspectRequest(request, service.environment); err != nil {
		return release.CandidateArtifactSet{}, err
	}

	scope := project.CandidateSourceScope{ProjectID: request.Scope.ProjectID, OwnerID: request.OwnerID}
	artifactBody, err := service.reader.OpenProjectArtifact(ctx, scope, request.Source.ArtifactDigest)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
	}
	artifactBytes, err := readNativeInspectBody(artifactBody, maxNativeInspectArtifactBytes, -1)
	if err != nil {
		if errors.Is(err, errNativeInspectLimit) {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
		}
		return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
	}
	compiledProject, err := projectartifact.Decode(artifactBytes)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if compiledProject.ProjectID() != request.Scope.ProjectID || compiledProject.Digest() != request.Source.ProjectDigest {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("retained project artifact does not match synchronized project"))
	}

	files, err := service.readSourceObjects(ctx, scope, request.Source)
	if err != nil {
		return release.CandidateArtifactSet{}, err
	}
	// The retained source tree and the retained compiler artifact are one
	// synchronized identity. Compile the logical bytes once to reject a reader
	// that serves an unrelated source set under the requested digest.
	authoredProject, err := projectcompiler.CompileProjectFiles(files, request.Source.ProjectFile)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if authoredProject.ProjectID() != compiledProject.ProjectID() || authoredProject.Digest() != compiledProject.Digest() {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("retained source files do not match project artifact"))
	}

	// Native serving state does not yet expose a durable object locator for the
	// active compiler artifact. Keep planning available for a target with no
	// base generation, and fail closed for requests that would need one.
	baseIdentity, err := request.Scope.BaseIdentity()
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if baseIdentity != nil {
		return release.CandidateArtifactSet{}, candidateArtifactUnavailable(errors.New("native candidate base artifact is unavailable"))
	}
	base := candidateGenerationBase{pins: map[string]string{}}
	plan, err := projectcompiler.PlanProjectFilesAgainstGraph(files, request.Source.ProjectFile, projectgraph.ProjectGraph{})
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	legacy := &candidateArtifactService{pins: service.pins, extensionPreparation: service.extensionPreparation}
	return legacy.inspectCandidateProjectPlan(ctx, request, compiledProject, plan, base)
}

func (service *nativeCandidateArtifactPhases) MaterializeCandidateArtifacts(ctx context.Context, request release.CandidateArtifactRequest, inspected release.CandidateArtifactSet) (release.CandidateArtifactSet, error) {
	if service == nil || service.artifacts == nil || service.storageDomain == "" {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	if !validNativeStorageDomain(service.storageDomain) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native candidate artifact storage security domain is invalid"))
	}
	if err := validateNativeInspectRequest(request, service.environment); err != nil {
		return release.CandidateArtifactSet{}, err
	}
	if err := validateNativeInspectedEvidence(request, inspected); err != nil {
		return release.CandidateArtifactSet{}, err
	}
	compiledProject := inspected.Compiler.Artifact
	plan := inspected.Compiler.Plan
	var content bytes.Buffer
	_, digest, err := projectbundle.PackCompiledProject(compiledProject, plan, &content)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if digest == "" || platformdigest.ValidateSHA256Identity(digest) != nil || int64(content.Len()) <= 0 || int64(content.Len()) > projectbundle.MaxBundleBytes {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("packed native serving artifact identity is invalid"))
	}
	if inspected.Artifact.ContentDigest != "" && inspected.Artifact.ContentDigest != digest || inspected.Generation.ArtifactDigest != "" && inspected.Generation.ArtifactDigest != digest || inspected.Generation.ServingArtifactID != "" && inspected.Generation.ServingArtifactID != nativeServingArtifactID(digest) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("inspected native serving artifact identity changed"))
	}
	expectedGenerationID := nativeCandidateGenerationID(request, digest).String()
	existingIdentity := inspected.Generation.Identity
	if existingIdentity.GenerationID != "" || existingIdentity.ProjectID != "" || existingIdentity.Environment != "" {
		expectedInspectID := "inspect-" + shortCandidateDigest(request.CandidateID)
		if existingIdentity.GenerationID != expectedInspectID && existingIdentity.GenerationID != expectedGenerationID || existingIdentity.ProjectID != request.Scope.ProjectID || existingIdentity.Environment != request.Scope.Environment {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("inspected native serving state identity changed"))
		}
	}
	key := nativeServingArtifactKey(digest)
	metadata := platformobjectstore.ObjectMetadata{
		StorageSecurityDomain: service.storageDomain,
		Digest:                digest,
		SizeBytes:             int64(content.Len()),
		ContentType:           nativeServingArtifactContentType,
		MetadataDigest:        nativeServingArtifactMetadataDigest(),
	}
	info, err := service.putServingArtifact(ctx, key, bytes.NewReader(content.Bytes()), metadata)
	if err != nil {
		return release.CandidateArtifactSet{}, err
	}
	if err := validateNativeServingArtifactInfo(info, key, metadata); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	generationID := nativeCandidateGenerationID(request, digest)
	identity, err := projectgraph.NewServingIdentity(request.Scope.ProjectID, request.Scope.Environment, generationID.String())
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	result := inspected
	result.Artifact.ContentDigest = digest
	result.Generation.Identity = identity
	result.Generation.ServingArtifactID = nativeServingArtifactID(digest)
	result.Generation.ArtifactDigest = digest
	if err := result.Generation.Identity.Validate(); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	return result, nil
}

func (service *nativeCandidateArtifactPhases) HydrateCandidateArtifacts(ctx context.Context, request release.CandidateArtifactRequest, inspected release.CandidateArtifactSet, identity release.CandidateArtifactIdentity) (release.CandidateArtifactSet, error) {
	if service == nil || service.artifacts == nil || service.storageDomain == "" {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	if !validNativeStorageDomain(service.storageDomain) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native candidate artifact storage security domain is invalid"))
	}
	if err := validateNativeInspectRequest(request, service.environment); err != nil {
		return release.CandidateArtifactSet{}, err
	}
	if err := validateNativeInspectedEvidence(request, inspected); err != nil {
		return release.CandidateArtifactSet{}, err
	}
	if err := validateNativeArtifactIdentity(identity); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if identity.ServingStateID != nativeCandidateGenerationID(request, identity.ServingArtifactDigest).String() {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native serving state identity does not match candidate artifact"))
	}
	if inspected.Artifact.ContentDigest != "" && inspected.Artifact.ContentDigest != identity.ServingArtifactDigest || inspected.Generation.ArtifactDigest != "" && inspected.Generation.ArtifactDigest != identity.ServingArtifactDigest || inspected.Generation.ServingArtifactID != "" && inspected.Generation.ServingArtifactID != identity.ServingArtifactID {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("inspected native serving artifact identity changed"))
	}
	if parsedGenerationID, parseErr := uuid.Parse(inspected.Generation.Identity.GenerationID); parseErr == nil && parsedGenerationID != uuid.Nil && inspected.Generation.Identity.GenerationID != identity.ServingStateID {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("inspected native serving state identity changed"))
	}
	key := nativeServingArtifactKey(identity.ServingArtifactDigest)
	metadata := platformobjectstore.ObjectMetadata{
		StorageSecurityDomain: service.storageDomain,
		Digest:                identity.ServingArtifactDigest,
		ContentType:           nativeServingArtifactContentType,
		MetadataDigest:        nativeServingArtifactMetadataDigest(),
	}
	object, err := service.artifacts.Open(ctx, key)
	if err != nil {
		return release.CandidateArtifactSet{}, nativeCandidateObjectError(err)
	}
	if object.Body == nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native serving artifact object body is nil"))
	}
	defer object.Body.Close()
	if object.Info.SizeBytes <= 0 || object.Info.SizeBytes > projectbundle.MaxBundleBytes {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native serving artifact object size is invalid"))
	}
	metadata.SizeBytes = object.Info.SizeBytes
	if err := validateNativeServingArtifactInfo(object.Info, key, metadata); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	validation, compiled, err := projectbundle.ValidateArtifactReader(object.Body, object.Info.SizeBytes)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if validation.Digest != identity.ServingArtifactDigest || validation.ProjectID != request.Scope.ProjectID.String() || validation.ProjectDigest != request.Source.ProjectDigest || compiled.ProjectID != request.Scope.ProjectID || compiled.ProjectDigest != request.Source.ProjectDigest {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native serving artifact compiled identity mismatch"))
	}
	if compiled.GraphDigest != inspected.Compiler.Graph.Digest() || !sameNativeJSON(compiled.Plan, inspected.Compiler.Plan) || !sameNativeJSON(compiled.Manifest, inspected.Compiler.Manifest) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native serving artifact compiler evidence mismatch"))
	}
	canonicalProject, err := projectartifact.NewProject(compiled.Graph, compiled.Manifest)
	if err != nil || canonicalProject.ProjectID() != request.Scope.ProjectID || canonicalProject.Digest() != request.Source.ProjectDigest {
		if err == nil {
			err = errors.New("native serving artifact project digest mismatch")
		}
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	var repacked bytes.Buffer
	_, repackedDigest, err := projectbundle.PackCompiledProject(canonicalProject, compiled.Plan, &repacked)
	if err != nil || repackedDigest != identity.ServingArtifactDigest || int64(repacked.Len()) != object.Info.SizeBytes {
		if err == nil {
			err = errors.New("native serving artifact canonical bytes do not match bound object")
		}
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	result := inspected
	result.Artifact.ContentDigest = identity.ServingArtifactDigest
	result.Compiler.Graph = compiled.Graph
	result.Compiler.Manifest = compiled.Manifest
	result.Compiler.Plan = compiled.Plan
	result.Compiler.Artifact = canonicalProject
	result.Generation.Identity, err = projectgraph.NewServingIdentity(request.Scope.ProjectID, request.Scope.Environment, identity.ServingStateID)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	result.Generation.ServingArtifactID = identity.ServingArtifactID
	result.Generation.ArtifactDigest = identity.ServingArtifactDigest
	if err := result.Generation.Identity.Validate(); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	return result, nil
}

func sameNativeJSON(left, right any) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func validateNativeInspectedEvidence(request release.CandidateArtifactRequest, inspected release.CandidateArtifactSet) error {
	if inspected.Artifact.SourceDigest != request.ArtifactDigest || inspected.Artifact.ProjectDigest != request.Source.ProjectDigest || inspected.Compiler.Artifact.ProjectID() != request.Scope.ProjectID || inspected.Compiler.Artifact.Digest() != request.Source.ProjectDigest || inspected.Compiler.Graph.ProjectID() != request.Scope.ProjectID || inspected.Compiler.Plan.Project != request.Scope.ProjectID.String() {
		return candidateArtifactInvalid(errors.New("inspected native compiler evidence does not match request"))
	}
	if inspected.Generation.Identity.ProjectID != request.Scope.ProjectID || inspected.Generation.Identity.Environment != request.Scope.Environment || inspected.Generation.Identity.Validate() != nil {
		return candidateArtifactInvalid(errors.New("inspected native serving scope does not match request"))
	}
	if err := inspected.Compiler.Graph.Validate(); err != nil {
		return candidateArtifactInvalid(err)
	}
	return nil
}

func validateNativeArtifactIdentity(identity release.CandidateArtifactIdentity) error {
	if identity.ServingArtifactID != nativeServingArtifactID(identity.ServingArtifactDigest) || platformdigest.ValidateSHA256Identity(identity.ServingArtifactDigest) != nil || identity.ServingStateID == "" || identity.ServingStateID != strings.TrimSpace(identity.ServingStateID) {
		return errors.New("native serving artifact identity is incomplete")
	}
	parsed, err := uuid.Parse(identity.ServingStateID)
	if err != nil || parsed == uuid.Nil {
		return errors.New("native serving state identity must be a UUID")
	}
	return nil
}

func (service *nativeCandidateArtifactPhases) putServingArtifact(ctx context.Context, key string, body io.Reader, metadata platformobjectstore.ObjectMetadata) (platformobjectstore.ObjectInfo, error) {
	info, err := service.artifacts.PutImmutable(ctx, key, body, metadata)
	if err == nil {
		if validateErr := validateNativeServingArtifactInfo(info, key, metadata); validateErr != nil {
			return platformobjectstore.ObjectInfo{}, candidateArtifactInvalid(validateErr)
		}
		return info, nil
	}
	if !errors.Is(err, platformobjectstore.ErrAmbiguous) {
		return platformobjectstore.ObjectInfo{}, nativeCandidateObjectError(err)
	}
	// The provider may have committed the object before losing its response.
	// Reconcile by exact-key read; only the original immutable identity is a
	// successful replay. A missing or unavailable object remains unavailable.
	object, openErr := service.artifacts.Open(ctx, key)
	if openErr != nil {
		return platformobjectstore.ObjectInfo{}, nativeCandidateObjectError(openErr)
	}
	if object.Body == nil {
		return platformobjectstore.ObjectInfo{}, candidateArtifactInvalid(errors.New("native serving artifact replay body is nil"))
	}
	defer object.Body.Close()
	if validateErr := validateNativeServingArtifactInfo(object.Info, key, metadata); validateErr != nil {
		return platformobjectstore.ObjectInfo{}, candidateArtifactInvalid(validateErr)
	}
	if _, _, validateErr := projectbundle.ValidateArtifactReader(object.Body, object.Info.SizeBytes); validateErr != nil {
		return platformobjectstore.ObjectInfo{}, candidateArtifactInvalid(validateErr)
	}
	return object.Info, nil
}

func nativeCandidateObjectError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, platformobjectstore.ErrInvalid) || errors.Is(err, platformobjectstore.ErrInvalidKey) || errors.Is(err, platformobjectstore.ErrConflict) || errors.Is(err, platformobjectstore.ErrDomainMismatch) || errors.Is(err, platformobjectstore.ErrCorrupt) {
		return candidateArtifactInvalid(err)
	}
	return candidateArtifactUnavailable(err)
}

func validateNativeServingArtifactInfo(info platformobjectstore.ObjectInfo, key string, expected platformobjectstore.ObjectMetadata) error {
	if info.Key != key || info.StorageSecurityDomain != expected.StorageSecurityDomain || info.Digest != expected.Digest || info.SizeBytes != expected.SizeBytes || info.ContentType != expected.ContentType || info.MetadataDigest != expected.MetadataDigest {
		return errors.New("native serving artifact object metadata mismatch")
	}
	return nil
}

func nativeServingArtifactKey(digest string) string {
	if platformdigest.ValidateSHA256Identity(digest) != nil {
		return ""
	}
	return nativeServingArtifactPrefix + strings.TrimPrefix(digest, "sha256:") + nativeServingArtifactSuffix
}

func nativeServingArtifactID(digest string) string {
	if platformdigest.ValidateSHA256Identity(digest) != nil {
		return ""
	}
	return "artifact-" + strings.TrimPrefix(digest, "sha256:")
}

func nativeServingArtifactMetadataDigest() string {
	sum := sha256.Sum256(nil)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validNativeStorageDomain(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 512 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func nativeCandidateGenerationID(request release.CandidateArtifactRequest, digest string) uuid.UUID {
	sum := sha256.Sum256([]byte("leapview-native-candidate-serving-state\x00" + request.CandidateID + "\x00" + request.Scope.ProjectID.String() + "\x00" + request.Scope.Environment + "\x00" + digest))
	var id uuid.UUID
	copy(id[:], sum[:16])
	// UUIDv5-shaped deterministic identity is always parseable by native
	// serving-state authorities.
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	return id
}

var errNativeInspectLimit = errors.New("native candidate inspect input exceeds bounded limit")
var errNativeInspectSize = errors.New("native candidate inspect object size mismatch")

func readNativeInspectBody(body io.ReadCloser, limit, expected int64) ([]byte, error) {
	if body == nil {
		return nil, errors.New("source reader returned nil body")
	}
	defer body.Close()
	if limit <= 0 {
		return nil, errNativeInspectLimit
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errNativeInspectLimit
	}
	if expected >= 0 && int64(len(data)) != expected {
		return nil, fmt.Errorf("%w: got %d, want %d", errNativeInspectSize, len(data), expected)
	}
	return data, nil
}

func validateNativeInspectRequest(request release.CandidateArtifactRequest, environment servingstate.Environment) error {
	if request.CandidateID == "" || request.CandidateID != strings.TrimSpace(request.CandidateID) || request.OwnerID == "" || request.OwnerID != strings.TrimSpace(request.OwnerID) || request.ArtifactDigest == "" || request.ArtifactDigest != strings.TrimSpace(request.ArtifactDigest) {
		return release.ErrCandidateArtifactInvalid
	}
	if request.Scope.Validate() != nil || request.Scope.Environment != string(environment) || request.Source.ProjectID.Validate() != nil || request.Source.ProjectID != request.Scope.ProjectID || request.Source.ArtifactDigest != request.ArtifactDigest || platformdigest.ValidateSHA256Identity(request.ArtifactDigest) != nil || request.Source.ProjectDigest == "" || request.Source.ProjectDigest != strings.TrimSpace(request.Source.ProjectDigest) || platformdigest.ValidateSHA256Identity(request.Source.ProjectDigest) != nil {
		return release.ErrCandidateArtifactInvalid
	}
	if !nativeInspectLogicalPath(request.Source.ProjectFile) {
		return candidateArtifactInvalid(errors.New("retained source project file is not canonical"))
	}
	return nil
}

func (service *nativeCandidateArtifactPhases) readSourceObjects(ctx context.Context, scope project.CandidateSourceScope, source project.CandidateSourceSnapshot) (map[string][]byte, error) {
	refs, err := service.reader.SourceObjectRefs(ctx, scope, source.ArtifactDigest)
	if err != nil {
		return nil, candidateArtifactUnavailable(err)
	}
	if len(refs) == 0 || len(refs) > maxNativeInspectSourceFiles {
		return nil, candidateArtifactInvalid(errors.New("retained source object set is empty or exceeds file limit"))
	}
	seen := make(map[string]struct{}, len(refs))
	files := make(map[string][]byte, len(refs))
	var total int64
	projectFileFound := false
	for _, ref := range refs {
		if !nativeInspectLogicalPath(ref.Path) || ref.SizeBytes < 0 || ref.SizeBytes > maxNativeInspectSourceBytes {
			return nil, candidateArtifactInvalid(errors.New("retained source object reference is invalid"))
		}
		if _, exists := seen[ref.Path]; exists {
			return nil, candidateArtifactInvalid(errors.New("retained source object set contains duplicate paths"))
		}
		seen[ref.Path] = struct{}{}
		if total > maxNativeInspectSourceBytes-ref.SizeBytes {
			return nil, candidateArtifactInvalid(errNativeInspectLimit)
		}
		total += ref.SizeBytes
		body, openErr := service.reader.OpenSourceObject(ctx, scope, ref)
		if openErr != nil {
			return nil, candidateArtifactUnavailable(openErr)
		}
		data, readErr := readNativeInspectBody(body, ref.SizeBytes, ref.SizeBytes)
		if readErr != nil {
			if errors.Is(readErr, errNativeInspectLimit) || errors.Is(readErr, errNativeInspectSize) {
				return nil, candidateArtifactInvalid(readErr)
			}
			return nil, candidateArtifactUnavailable(readErr)
		}
		actual := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
		if platformdigest.ValidateSHA256Identity(ref.Digest) != nil || actual != ref.Digest {
			return nil, candidateArtifactInvalid(errors.New("retained source object digest mismatch"))
		}
		files[ref.Path] = data
		if ref.Path == source.ProjectFile {
			projectFileFound = true
		}
	}
	if !projectFileFound {
		return nil, candidateArtifactInvalid(errors.New("retained source project file is missing"))
	}
	return files, nil
}

func nativeInspectLogicalPath(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 1024 || !utf8.ValidString(value) || strings.Contains(value, `\`) || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || len(segment) >= 2 && segment[1] == ':' {
			return false
		}
	}
	return true
}
