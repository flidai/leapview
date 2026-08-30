// Package http exposes project-scoped deployment operations over the generated API.
package http

import (
	"context"
	"log/slog"
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
	Coordinator         Coordinator
	CurrentPrincipal    func(*stdhttp.Request) (Principal, bool)
	MaxJSONBodyBytes    int64
	InstanceEnvironment string
	Logger              *slog.Logger
}

type Handler struct {
	options Options
}

func NewHandler(options Options) *Handler {
	if options.MaxJSONBodyBytes <= 0 {
		options.MaxJSONBodyBytes = 1 << 20
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Handler{options: options}
}

func (h *Handler) Principal(r *stdhttp.Request) (Principal, bool) { return h.principal(r) }

func (h *Handler) Environment() string {
	if h == nil {
		return ""
	}
	return h.options.InstanceEnvironment
}
