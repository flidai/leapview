package http

import (
	"context"

	"github.com/flidai/leapview/internal/access"
)

func allowProjectAdmin(context.Context, string) ([]access.Capability, error) {
	return []access.Capability{access.CapabilityProjectAdmin}, nil
}
