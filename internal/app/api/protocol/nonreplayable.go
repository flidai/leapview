package protocol

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// ValidateNonReplayableRequest rejects every supplied idempotency header,
// including empty or repeated values, without reading or buffering the body.
// Call it only for a server-selected generated replay-forbidden command;
// authentication, authorization, CSRF and body limits remain required.
func ValidateNonReplayableRequest(w http.ResponseWriter, r *http.Request) bool {
	for name := range r.Header {
		if strings.EqualFold(name, "Idempotency-Key") {
			apitransport.WriteProblem(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_FORBIDDEN", "This command does not accept Idempotency-Key and must not be automatically retried.", nil)
			return false
		}
	}
	return true
}

// BrowserNonReplayableMutationMiddleware binds a dedicated browser POST route
// to a generated non-replayable command. Mount session authentication, CSRF and
// ingress limits outside it; authorize must check current exact-resource access.
// The caller's operation claim is verified but can never select a replay policy.
func (p *Protocol) BrowserNonReplayableMutationMiddleware(binding uicommand.Binding, authorize func(*http.Request) bool, next http.Handler) (http.Handler, error) {
	return p.browserNonReplayableMutationMiddleware(binding, authorize, next, apiaggregate.GetAPIGenOperationContract)
}

func (p *Protocol) browserNonReplayableMutationMiddleware(binding uicommand.Binding, authorize func(*http.Request) bool, next http.Handler, lookup func(string) (apiaggregate.GenOperationContract, bool)) (http.Handler, error) {
	if p == nil || !binding.Valid() || !binding.ReplayForbidden() || authorize == nil || next == nil || lookup == nil {
		return nil, errors.New("non-replayable browser command requires a generated binding, authorization and handler")
	}
	contract, ok := lookup(binding.OperationID())
	if !ok || contract.OperationID != binding.OperationID() || contract.Method != http.MethodPost || contract.Kind != apiaggregate.GenOperationKindCommand ||
		contract.Command == nil || contract.Command.Idempotency != "forbidden" || contract.Command.UI == nil || contract.Command.UI.ActionID != binding.ActionID() ||
		!slices.Contains(contract.Command.AdditionalExposures, apiaggregate.GenOperationSurfaceUI) {
		return nil, errors.New("browser command does not declare the generated non-replayable UI policy")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			apitransport.WriteProblem(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "This command requires POST.", nil)
			return
		}
		if !authorize(r) {
			apitransport.WriteProblem(w, r, http.StatusForbidden, "FORBIDDEN", "Permission to execute this command is required.", nil)
			return
		}
		if !ValidateNonReplayableRequest(w, r) {
			return
		}
		if !canonicalUUIDv7(strings.TrimSpace(r.Header.Get("X-Request-ID"))) {
			apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_ID", "X-Request-ID must be a canonical UUIDv7 value.", nil)
			return
		}
		if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), binding.OperationID()); err != nil {
			apitransport.WriteProblem(w, r, http.StatusBadRequest, "COMMAND_OPERATION_MISMATCH", "The operation claim must match the dispatched command.", nil)
			return
		}
		PrepareRequest(w, r)
		next.ServeHTTP(w, r)
	}), nil
}
