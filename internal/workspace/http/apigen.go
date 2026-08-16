package http

import (
	"log/slog"
	stdhttp "net/http"
)

// DispatchAPIGenOperation intentionally does not register the retired
// workspace API surface. Workspace-owned browser helpers remain available via
// their direct module ports, while public project APIs are dispatched by their
// owning generated package.
func DispatchAPIGenOperation(_ string, _ *slog.Logger, _ stdhttp.ResponseWriter, _ *stdhttp.Request) bool {
	return false
}
