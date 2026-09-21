// Package module exposes the development-session capability to application
// composition without making the app depend on the capability's persistence
// or transport implementation packages directly.
package module

import (
	"context"

	"github.com/flidai/leapview/internal/project/developmentsession"
	developmenthttp "github.com/flidai/leapview/internal/project/developmentsession/http"
)

type Store = developmentsession.Store
type Identity = developmentsession.Identity
type Handler = developmenthttp.Handler
type Config = developmenthttp.Config
type CandidateValidation = developmenthttp.CandidateValidation

var (
	ErrNotFound      = developmentsession.ErrNotFound
	ErrOwnerMismatch = developmentsession.ErrOwnerMismatch
)

// Module is the application composition surface for development sessions.
// Persistence and transport construction remain owned by this capability.
type Module struct {
	handler *developmenthttp.Handler
}

// Build constructs the capability through the repository-standard module
// entrypoint. The handler itself remains fail-closed when the capability is
// disabled or no durable store is configured.
func Build(_ context.Context, config Config) *Module {
	return &Module{handler: developmenthttp.New(config)}
}

// HTTP exposes the authenticated development-session transport surface.
func (m *Module) HTTP() *Handler {
	if m == nil {
		return nil
	}
	return m.handler
}
