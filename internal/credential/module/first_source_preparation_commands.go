package module

import (
	"context"
	"time"

	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// FirstSourcePreparationCommandRequest retains only explicit non-secret intent.
// Publisher and source owner are always the authenticated actor, never input.
type FirstSourcePreparationCommandRequest struct {
	PreparationID, VersionID, ReceiptID   string
	SourceDigest, SourceAttestationDigest string
	PlanIdempotencyKey                    string
	ExpectedTargetRevision                int64
}

func FirstSourcePreparationBrowserBinding(operation string) (uicommand.Binding, bool) {
	switch operation {
	case "abortCredentialActivation":
		return credentialgen.GenUIActionAbortCredentialActivation(), true
	case "saveCredentialDraft":
		return credentialgen.GenUIActionSaveCredentialDraft(), true
	case "validateCredentialDraft":
		return credentialgen.GenUIActionValidateCredentialDraft(), true
	case "prepareFirstSourceCredential":
		return credentialgen.GenUIActionPrepareFirstSourceCredential(), true
	case "renewFirstSourceCredentialPreparation":
		return credentialgen.GenUIActionRenewFirstSourceCredentialPreparation(), true
	default:
		return uicommand.Binding{}, false
	}
}

type FirstSourcePreparationCommandResult struct {
	PreparationID, IntentDigest, PlanRequestDigest string
	CreatedAt                                      time.Time
}

// FirstSourcePreparationCommandService exposes bounded preparation metadata.
// Implementations recheck live session, exact manage/use, retained source and
// unpublished target authority; preparing does not grant publication authority.
type FirstSourcePreparationCommandService interface {
	Authorize(context.Context, string, ValidationResource) error
	Prepare(context.Context, string, ValidationResource, FirstSourcePreparationCommandRequest) (FirstSourcePreparationCommandResult, error)
	Renew(context.Context, string, ValidationResource, string, string) (FirstSourcePreparationCommandResult, error)
}

type FirstSourceBrowserDescription struct {
	Host, Database, SourceIdentity string
	TargetRevision                 int64
}

// Description is an independently authorized metadata read for the admitted
// browser operator. It must not expose credential reference or secret bytes.
type FirstSourceBrowserDescriber interface {
	Describe(context.Context, string, ValidationResource) (FirstSourceBrowserDescription, error)
}
