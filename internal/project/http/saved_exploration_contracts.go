package http

import (
	"context"

	savedexploration "github.com/flidai/leapview/internal/analytics/exploration/saved"
	savedapplication "github.com/flidai/leapview/internal/analytics/exploration/saved/application"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// SavedExplorationService is the narrow application boundary used by the
// authenticated browser. It intentionally has no Execute method: reopening
// returns authored state, while the existing governed Data Explorer path owns
// the one analytical execution.
type SavedExplorationService interface {
	AuthorizeMutationReplay(context.Context, savedapplication.MutationReplayAuthorizationRequest) (bool, error)
	Create(context.Context, savedexploration.CreateRequest) (savedexploration.MutationResult, error)
	UpdateVersion(context.Context, savedexploration.UpdateVersionRequest) (savedexploration.MutationResult, error)
	Duplicate(context.Context, savedexploration.DuplicateRequest) (savedexploration.MutationResult, error)
	List(context.Context, savedexploration.ListRequest) ([]savedexploration.Lifecycle, error)
	Archive(context.Context, savedexploration.ArchiveRequest) (savedexploration.MutationResult, error)
	Reopen(context.Context, savedexploration.ReopenRequest) (savedexploration.ReopenResult, error)
}

// SavedExplorationCommandBindings are generated operation claims projected
// into the browser. The handler verifies the claimed action before any
// lifecycle or repository call.
type SavedExplorationCommandBindings struct {
	Create    uicommand.Binding
	Update    uicommand.Binding
	Duplicate uicommand.Binding
	Archive   uicommand.Binding
}

// SavedExplorationCommandInvocation preserves the complete revision token
// while starting the generated command runtime. A revision number alone is
// not a valid CAS or concurrency token.
type SavedExplorationCommandInvocation struct {
	Action              string
	Project             string
	Resource            string
	IdempotencyKey      string
	RequestID           string
	CorrelationID       string
	Revision            savedexploration.RevisionToken
	ConcurrencyRevision *savedexploration.RevisionToken
}
