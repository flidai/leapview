package http

import (
	stdhttp "net/http"

	"github.com/flidai/leapview/internal/access"
)

// ListRoles returns the immutable project role catalog. Role definitions are
// contract data, rather than instance records: the response never consults
// the access repository and does not expose a project selector.
func (h Handler) ListRoles(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	roles := access.CanonicalProjectRoles()
	items := make([]map[string]any, 0, len(roles))
	for _, role := range roles {
		capabilities := access.ProjectRoleCapabilities(role)
		items = append(items, map[string]any{
			"name":         string(role),
			"capabilities": capabilities,
		})
	}
	_ = writePagedJSON(w, r, items)
}
