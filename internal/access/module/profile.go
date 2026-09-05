package module

import (
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/http/mcpoauth"
)

// ProfileSurface is the bounded, persistence-free authority used by profile
// and application test assemblies. Its adapters stay private to the access
// module; production composition must use Persistence instead.
//
// The surface intentionally carries no OAuth state store. A resource verifier
// may authenticate bounded test tokens, while durable MCP OAuth remains owned
// by the PostgreSQL Persistence bundle.
type ProfileSurface struct {
	repository    access.Repository
	oauthResource mcpoauth.ResourceServer
}

// NewProfileSurface constructs a bounded profile authority from the supplied
// repository reads and MCP resource verifier. Build rejects this surface for
// production or persistence-backed assemblies.
func NewProfileSurface(repository access.Repository, oauthResource mcpoauth.ResourceServer) *ProfileSurface {
	return &ProfileSurface{repository: repository, oauthResource: oauthResource}
}
