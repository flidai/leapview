package module

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/credential"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/pkg/strictjson"
)

func (d credentialDraftAPIGenDispatcher) ValidateCredentialDraft(w http.ResponseWriter, r *http.Request, project, target, connection, version string) {
	d.validateCredentialDraft(w, r, project, target, connection, version, apigencommand.SurfaceAPI)
}

func (d credentialDraftAPIGenDispatcher) validateCredentialDraft(w http.ResponseWriter, r *http.Request, project, target, connection, version string, surface apigencommand.Surface) {
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := d.principal(w, r)
	if !ok {
		return
	}
	if d.config.Validation == nil {
		writeCredentialDraftUnavailable(w, r)
		return
	}
	revision, ok := decodeCredentialValidationRevision(w, r)
	if !ok {
		return
	}
	invocation := credentialgen.GenValidateCredentialDraftCommandInvocation{
		Surface: surface, Connection: connection,
		RequestID:     strings.TrimSpace(r.Header.Get("X-Request-ID")),
		CorrelationID: strings.TrimSpace(r.Header.Get("X-Correlation-ID")),
	}
	executor, err := apigencommand.NewExecutor(credentialgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		writeCredentialValidationError(w, r, err)
		return
	}
	var receipt credential.ValidationReceipt
	err = credentialgen.ExecuteGenValidateCredentialDraftCommand(r.Context(), executor, invocation, apigencommand.Execution{
		// The service probes without a database transaction, then commits the
		// immutable receipt and audit together. The transport guard records that
		// completed transaction; it never treats validation as pool activation.
		Transactional: func(ctx context.Context, _ apigencommand.Contract) error {
			var validateErr error
			receipt, validateErr = d.config.Validation.ValidateDraft(ctx, actor, d.resource(project, target, connection), version, revision)
			return validateErr
		},
	})
	if err != nil {
		writeCredentialValidationError(w, r, err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, credentialgen.CredentialValidationResponse{
		ReceiptId: receipt.ReceiptID, VersionId: receipt.Binding.VersionID,
		BindingRevision: receipt.BindingRevision,
		ValidatedAt:     receipt.ValidatedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:       receipt.ExpiresAt.UTC().Format(time.RFC3339Nano), State: "validated",
	})
}

func decodeCredentialValidationRevision(w http.ResponseWriter, r *http.Request) (int64, bool) {
	const maxBytes int64 = 1024
	if r.ContentLength > maxBytes {
		writeCredentialDraftBodyTooLarge(w, r)
		return 0, false
	}
	var body map[string]json.RawMessage
	err := strictjson.DecodeReader(r.Body, &body, strictjson.Options{MaxBytes: maxBytes, MaxDepth: 2})
	if errors.Is(err, strictjson.ErrSizeLimit) {
		writeCredentialDraftBodyTooLarge(w, r)
		return 0, false
	}
	var revision int64
	if err != nil || len(body) != 1 || json.Unmarshal(body["expectedBindingRevision"], &revision) != nil || revision < 1 {
		writeCredentialDraftInvalidBody(w, r)
		return 0, false
	}
	return revision, true
}

func writeCredentialValidationError(w http.ResponseWriter, r *http.Request, err error) {
	kind := "provider_unavailable"
	switch {
	case errors.Is(err, credential.ErrInvalid):
		kind = "invalid"
	case errors.Is(err, credential.ErrForbidden):
		kind = "unauthorized"
	case errors.Is(err, credential.ErrNotFound):
		kind = "not_found"
	case errors.Is(err, credential.ErrConflict):
		kind = "conflict"
	case errors.Is(err, credential.ErrValidationFailed):
		kind = "credential_invalid"
	}
	apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, nil,
		credentialgen.GenCommandOperationValidateCredentialDraft(), credentialgen.GetAPIGenCommandFailureContracts,
		apigenfailure.New(kind, "credential validation failed"))
}
