// Package http contains small transport contracts shared by LeapView HTTP
// surfaces. It deliberately does not assign request identity semantics to a
// caller; it only applies an ordered, trimmed header preference.
package http

import (
	"net/http"
	"strings"
)

// FirstNonEmptyHeader returns the first named request header whose trimmed
// value is non-empty. Header names are intentionally tried in caller order so
// compatibility aliases can have an explicit precedence. A nil request is
// treated as having no headers.
func FirstNonEmptyHeader(request *http.Request, names ...string) string {
	if request == nil {
		return ""
	}
	for _, name := range names {
		if value := strings.TrimSpace(request.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

// AuditRequestIdentity returns the stable request and correlation identities
// used by HTTP audit surfaces. Correlation falls back to request identity when
// the caller omits it; both header spellings remain explicitly ordered for
// compatibility with older clients.
func AuditRequestIdentity(request *http.Request) (requestID, correlationID string) {
	requestID = FirstNonEmptyHeader(request, "X-Request-ID", "X-Request-Id")
	correlationID = FirstNonEmptyHeader(request, "X-Correlation-ID", "X-Correlation-Id")
	if correlationID == "" {
		correlationID = requestID
	}
	return requestID, correlationID
}
