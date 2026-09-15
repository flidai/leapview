// Package module exposes the development-session capability to application
// composition without making the app depend on the capability's persistence
// or transport implementation packages directly.
package module

import (
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

func New(config Config) *Handler { return developmenthttp.New(config) }
