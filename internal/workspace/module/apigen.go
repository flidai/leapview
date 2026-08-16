package module

import (
	"log/slog"
	"net/http"
)

// DispatchAPIGenOperation preserves the module seam for callers that have not
// yet removed the retired workspace operation ID. No workspace API routes are
// registered; project-owned APIs use their generated dispatchers.
func (m *Module) DispatchAPIGenOperation(_ string, _ *slog.Logger, _ http.ResponseWriter, _ *http.Request) bool {
	return false
}
