package application

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	configschema "github.com/flidai/leapview/internal/project/schema"
	"github.com/pmezard/go-difflib/difflib"
)

const (
	maxDashboardSourceBytes = 4 << 20
	maxDashboardSourceEdits = 64
)

// SourceEdit is one exact replacement against the canonical YAML returned by
// ReadSource. OldText must identify exactly one range in that original source.
type SourceEdit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// SourceRead returns the current editable draft as canonical dashboard YAML.
type SourceRead struct {
	DashboardID authoring.DashboardID   `json:"dashboardId"`
	DraftID     authoring.DraftID       `json:"draftId"`
	Revision    authoring.RevisionToken `json:"revision"`
	YAML        string                  `json:"yaml"`
}

// SourceEditRequest binds exact replacements to one immutable draft revision.
// Provenance is server-owned at the transport boundary.
type SourceEditRequest struct {
	ProjectID        projectgraph.ResourceID
	ActorID          string
	DashboardID      authoring.DashboardID
	DraftID          authoring.DraftID
	ExpectedRevision authoring.RevisionToken
	Edits            []SourceEdit
	CommandID        authoring.CommandID
	Provenance       authoring.Provenance
}

// SourceEditResult is the committed revision plus reviewable canonical source
// and a unified diff. Embedding Result preserves the ordinary authoring result
// wire shape used by the other draft mutation tools.
type SourceEditResult struct {
	authoringservice.Result
	YAML          string `json:"yaml"`
	Diff          string `json:"diff"`
	ChangedBlocks int    `json:"changedBlocks"`
}

// ReadSource exposes only the current private draft and requires EDIT access.
func (a *Application) ReadSource(ctx context.Context, request DraftRequest) (SourceRead, error) {
	read, err := a.Draft(ctx, request)
	if err != nil {
		return SourceRead{}, err
	}
	encoded, err := document.EncodeYAML(read.Revision.Document)
	if err != nil {
		return SourceRead{}, fmt.Errorf("encode dashboard draft source: %w", err)
	}
	if len(encoded) > maxDashboardSourceBytes {
		return SourceRead{}, fmt.Errorf("%w: dashboard source exceeds %d bytes", authoring.ErrInvalidPayload, maxDashboardSourceBytes)
	}
	return SourceRead{DashboardID: read.Lifecycle.ID, DraftID: read.Lifecycle.Draft.ID, Revision: read.Revision.Token(), YAML: string(encoded)}, nil
}

// EditSource applies exact text replacements to a retained revision, decodes
// the result through the generated dashboard schema, then commits it through
// the ordinary transactional authoring service. Service-level optimistic
// concurrency remains authoritative; loading a retained revision here also
// lets a retry with the same command ID replay after the draft has advanced.
func (a *Application) EditSource(ctx context.Context, request SourceEditRequest) (SourceEditResult, error) {
	projectID, revision, err := a.sourceEditRevision(ctx, request)
	if err != nil {
		return SourceEditResult{}, err
	}
	before, err := document.EncodeYAML(revision.Document)
	if err != nil {
		return SourceEditResult{}, fmt.Errorf("encode dashboard draft source: %w", err)
	}
	edited, err := applyExactSourceEdits(string(before), request.Edits)
	if err != nil {
		return SourceEditResult{}, err
	}
	if len(edited) > maxDashboardSourceBytes {
		return SourceEditResult{}, fmt.Errorf("%w: edited dashboard source exceeds %d bytes", authoring.ErrInvalidPayload, maxDashboardSourceBytes)
	}
	var replacement document.DashboardDocument
	if err := configschema.DecodeResource(configschema.KindDashboard, "dashboard.yaml", []byte(edited), &replacement); err != nil {
		return SourceEditResult{}, fmt.Errorf("%w: decode edited dashboard source: %v", authoring.ErrInvalidPayload, err)
	}
	if replacement.Metadata.ID != revision.Document.Metadata.ID || replacement.Metadata.Name != revision.Document.Metadata.Name {
		return SourceEditResult{}, fmt.Errorf("%w: edited source cannot change dashboard resource identity", authoring.ErrInvalidPayload)
	}
	if replacement.Spec.SemanticModel != revision.Document.Spec.SemanticModel {
		return SourceEditResult{}, fmt.Errorf("%w: edited source cannot change the dashboard semantic model", authoring.ErrInvalidPayload)
	}
	after, err := document.EncodeYAML(replacement)
	if err != nil {
		return SourceEditResult{}, fmt.Errorf("encode edited dashboard source: %w", err)
	}
	if string(after) == string(before) {
		return SourceEditResult{}, fmt.Errorf("%w: edits do not change the canonical dashboard source", authoring.ErrInvalidPayload)
	}
	command := authoring.Command{
		ID: request.CommandID, DashboardID: request.DashboardID, DraftID: request.DraftID,
		ExpectedRevision: request.ExpectedRevision, Provenance: request.Provenance,
		ReplaceDocument: &authoring.ReplaceDocumentPayload{Document: replacement},
	}
	result, err := a.authoring.Execute(ctx, projectID, command)
	if err != nil {
		return SourceEditResult{}, err
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(string(before)), B: difflib.SplitLines(string(after)),
		FromFile: "dashboard.yaml", ToFile: "dashboard.yaml", Context: 3,
	})
	if err != nil {
		return SourceEditResult{}, fmt.Errorf("render dashboard source diff: %w", err)
	}
	return SourceEditResult{Result: result, YAML: string(after), Diff: diff, ChangedBlocks: len(request.Edits)}, nil
}

func (a *Application) sourceEditRevision(ctx context.Context, request SourceEditRequest) (projectgraph.ResourceID, authoring.Revision, error) {
	if err := a.validate(); err != nil {
		return "", authoring.Revision{}, err
	}
	projectID, err := projectID(request.ProjectID)
	if err != nil {
		return "", authoring.Revision{}, err
	}
	actorID := strings.TrimSpace(request.ActorID)
	if actorID == "" || strings.TrimSpace(request.Provenance.ActorID) != actorID {
		return "", authoring.Revision{}, fmt.Errorf("actor id and matching command provenance are required")
	}
	if err := request.CommandID.Validate(); err != nil {
		return "", authoring.Revision{}, err
	}
	if err := request.Provenance.Validate(); err != nil {
		return "", authoring.Revision{}, err
	}
	if err := authoring.ValidateDashboardID(request.DashboardID); err != nil {
		return "", authoring.Revision{}, err
	}
	if err := request.DraftID.Validate(); err != nil {
		return "", authoring.Revision{}, err
	}
	if err := request.ExpectedRevision.ValidateComplete(); err != nil {
		return "", authoring.Revision{}, err
	}
	lifecycle, err := a.repository.Get(ctx, projectID, request.DashboardID)
	if err != nil {
		return "", authoring.Revision{}, err
	}
	if err := a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actorID, ProjectID: projectID, DashboardID: request.DashboardID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel,
		Target: authoringservice.AuthorizationTargetAuthoredDashboard, Visibility: lifecycle.Visibility,
		Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		return "", authoring.Revision{}, err
	}
	if err := lifecycle.Validate(); err != nil {
		return "", authoring.Revision{}, fmt.Errorf("validate dashboard source lifecycle: %w", err)
	}
	if lifecycle.ProjectID != projectID || lifecycle.ID != request.DashboardID || lifecycle.Draft == nil || lifecycle.Draft.ID != request.DraftID {
		return "", authoring.Revision{}, fmt.Errorf("%w: source edit does not select the dashboard draft", authoring.ErrStaleRevision)
	}
	revision, err := a.repository.GetRevision(ctx, projectID, request.DashboardID, request.ExpectedRevision.RevisionID)
	if err != nil {
		return "", authoring.Revision{}, err
	}
	if err := revision.Validate(); err != nil {
		return "", authoring.Revision{}, fmt.Errorf("validate dashboard source revision: %w", err)
	}
	if revision.DashboardID != request.DashboardID || !sameRevision(revision.Token(), request.ExpectedRevision) {
		return "", authoring.Revision{}, fmt.Errorf("%w: source edit revision does not match the retained revision", authoring.ErrStaleRevision)
	}
	return projectID, revision, nil
}

type sourceEditMatch struct {
	start int
	end   int
	edit  SourceEdit
}

func applyExactSourceEdits(source string, edits []SourceEdit) (string, error) {
	if len(edits) == 0 || len(edits) > maxDashboardSourceEdits {
		return "", fmt.Errorf("%w: source edit requires 1-%d replacements", authoring.ErrInvalidPayload, maxDashboardSourceEdits)
	}
	matches := make([]sourceEditMatch, 0, len(edits))
	resultBytes := len(source)
	for index, edit := range edits {
		if edit.OldText == "" {
			return "", fmt.Errorf("%w: source edit %d oldText cannot be empty", authoring.ErrInvalidPayload, index)
		}
		if edit.OldText == edit.NewText {
			return "", fmt.Errorf("%w: source edit %d does not change text", authoring.ErrInvalidPayload, index)
		}
		resultBytes += len(edit.NewText) - len(edit.OldText)
		start := strings.Index(source, edit.OldText)
		if start < 0 {
			return "", fmt.Errorf("%w: source edit %d oldText was not found", authoring.ErrInvalidPayload, index)
		}
		if strings.Contains(source[start+len(edit.OldText):], edit.OldText) {
			return "", fmt.Errorf("%w: source edit %d oldText is ambiguous", authoring.ErrInvalidPayload, index)
		}
		matches = append(matches, sourceEditMatch{start: start, end: start + len(edit.OldText), edit: edit})
	}
	if resultBytes > maxDashboardSourceBytes {
		return "", fmt.Errorf("%w: edited dashboard source exceeds %d bytes", authoring.ErrInvalidPayload, maxDashboardSourceBytes)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].start < matches[j].start })
	for index := 1; index < len(matches); index++ {
		if matches[index].start < matches[index-1].end {
			return "", fmt.Errorf("%w: source edits overlap", authoring.ErrInvalidPayload)
		}
	}
	result := source
	for index := len(matches) - 1; index >= 0; index-- {
		match := matches[index]
		result = result[:match.start] + match.edit.NewText + result[match.end:]
	}
	return result, nil
}
