package module

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/extension"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/project"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
)

// nativeCandidateArtifactPhases is deliberately limited to the read-only
// inspect phase. A native source reader supplies immutable bytes by object
// identity; no serving-state/artifact writer is available to this adapter.
type nativeCandidateArtifactPhases struct {
	reader               project.CandidateSourceObjectReader
	environment          servingstate.Environment
	pins                 ManagedDataPins
	extensionPreparation extension.Preparation
}

var _ candidateArtifactPhases = (*nativeCandidateArtifactPhases)(nil)

const (
	maxNativeInspectArtifactBytes int64 = 64 << 20
	maxNativeInspectSourceBytes   int64 = 64 << 20
	maxNativeInspectSourceFiles         = 10_000
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

func (service *nativeCandidateArtifactPhases) MaterializeCandidateArtifacts(context.Context, release.CandidateArtifactRequest, release.CandidateArtifactSet) (release.CandidateArtifactSet, error) {
	return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
}

func (service *nativeCandidateArtifactPhases) HydrateCandidateArtifacts(context.Context, release.CandidateArtifactRequest, release.CandidateArtifactSet, release.CandidateArtifactIdentity) (release.CandidateArtifactSet, error) {
	return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
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
