package http

import (
	stdhttp "net/http"
)

// CreateDataPolicy rejects new standalone policy creation through this API
// while leaving the historical DataPolicy artifact and runtime compatibility
// paths untouched.
// Authentication and capability checks remain owned by the outer APIGen
// middleware before this handler is reached.
func (h Handler) CreateDataPolicy(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	writeAPIProblem(w, r, stdhttp.StatusConflict, "DATA_POLICY_AUTHORING_RESTRICTED", "New standalone DataPolicy creation through this API is restricted; migrate to SemanticModel access policy after activation qualification.", nil)
}
