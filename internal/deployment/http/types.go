// Package http exposes project-scoped deployment operations over the generated API.
package http

import (
	"context"
	stdhttp "net/http"

	"github.com/flidai/leapview/internal/deployment/apiadapter"
)

type Principal struct {
	ID string
}

type Coordinator interface {
	Create(context.Context, apiadapter.CreateRequest) (apiadapter.Deployment, error)
	Get(context.Context, apiadapter.Scope) (apiadapter.Deployment, error)
	Activate(context.Context, apiadapter.ActivateRequest) (apiadapter.Deployment, error)
	CancelRequest(context.Context, apiadapter.CancelRequest) (apiadapter.Deployment, error)
}

type Options struct {
	CurrentPrincipal    func(*stdhttp.Request) (Principal, bool)
	InstanceEnvironment string
}

type Handler struct {
	options Options
}

func NewHandler(options Options) *Handler {
	return &Handler{options: options}
}

func (h *Handler) Principal(r *stdhttp.Request) (Principal, bool) { return h.principal(r) }

func (h *Handler) Environment() string {
	if h == nil {
		return ""
	}
	return h.options.InstanceEnvironment
}
