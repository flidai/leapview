package apigenruntime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apiprotocol "github.com/flidai/leapview/internal/app/api/protocol"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/go-chi/chi/v5"
)

type Authorizer interface {
	Protect(operationID string, next http.Handler) (http.Handler, bool)
}

type Handler struct {
	authorizer Authorizer
	dispatch   Dispatch
	commands   apigencommand.Lookup
}

const maxGeneratedJSONBodyBytes int64 = 16 << 20

type Dispatch func(operationID string, w http.ResponseWriter, r *http.Request) bool

func Build(authorizer Authorizer, dispatch Dispatch, commands apigencommand.Lookup) (*Handler, error) {
	if authorizer == nil {
		return nil, fmt.Errorf("APIGen authorizer is required")
	}
	if dispatch == nil {
		return nil, fmt.Errorf("APIGen dispatch function is required")
	}
	if commands == nil {
		return nil, fmt.Errorf("APIGen command contract lookup is required")
	}
	return &Handler{authorizer: authorizer, dispatch: dispatch, commands: commands}, nil
}

func (h *Handler) HandleAPIGen(operationID string, w http.ResponseWriter, r *http.Request) {
	// Keep the generated boundary self-contained: callers that mount a
	// partition without the public-protocol middleware still receive the
	// canonical request identifier contract.
	apiprotocol.PrepareRequest(w, r)
	protected, ok := h.authorizer.Protect(operationID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validateBoundary(operationID, w, r) {
			return
		}
		var guard *apigencommand.Guard
		if h.commands != nil {
			if contract, command := h.commands(operationID); command {
				surface := apigencommand.SurfaceAPI
				if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-LeapView-Invocation-Surface")), string(apigencommand.SurfaceCLI)) ||
					strings.EqualFold(strings.TrimSpace(r.Header.Get("X-LeapView-Client")), string(apigencommand.SurfaceCLI)) {
					surface = apigencommand.SurfaceCLI
				}
				targets := map[string]string{}
				if contract.Target != nil {
					targets[contract.Target.Parameter] = chi.URLParam(r, contract.Target.Parameter)
				}
				ctx, started, err := apigencommand.BeginInvocation(r.Context(), contract, apigencommand.Invocation{
					Surface: surface, TargetValues: targets,
					IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")), ConcurrencyToken: strings.TrimSpace(r.Header.Get("If-Match")),
					RequestID: strings.TrimSpace(r.Header.Get("X-Request-ID")), CorrelationID: strings.TrimSpace(r.Header.Get("X-Correlation-ID")),
				})
				if err != nil {
					writeInvocationProblem(w, r, err)
					return
				}
				r = r.WithContext(ctx)
				guard = started
			}
		}
		buffered := apiprotocol.NewResponseBuffer(w, r)
		if ok := h.dispatch(operationID, buffered, r); !ok {
			http.NotFound(w, r)
			return
		}
		if guard != nil && buffered.StatusCode() >= 200 && buffered.StatusCode() < 300 && !guard.Completed() {
			apitransport.WriteProblem(w, r, http.StatusInternalServerError, "COMMAND_CONTRACT_NOT_EXECUTED", "The command did not execute through the generated command contract.", nil)
			return
		}
		buffered.Flush()
	}))
	if !ok {
		http.NotFound(w, r)
		return
	}
	protected.ServeHTTP(w, r)
}

func writeInvocationProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, apigencommand.ErrIdempotencyRequired):
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key is required by the command contract.", nil)
	case errors.Is(err, apigencommand.ErrPreconditionRequired):
		apitransport.WriteProblem(w, r, http.StatusPreconditionFailed, "PRECONDITION_REQUIRED", "If-Match is required by the command contract.", nil)
	case errors.Is(err, apigencommand.ErrSurfaceNotExposed):
		apitransport.WriteProblem(w, r, http.StatusForbidden, "COMMAND_SURFACE_FORBIDDEN", "The command is not exposed through this invocation surface.", nil)
	case errors.Is(err, apigencommand.ErrTargetRequired):
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "COMMAND_TARGET_REQUIRED", "The command authorization target is required.", nil)
	default:
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "INVALID_COMMAND_CONTRACT", "The command contract is invalid.", nil)
	}
}

// validateBoundary owns the small set of transport constraints that must be
// enforced before any generated dispatcher (and therefore any domain
// service) is entered. Generated partitions intentionally only bind values;
// this keeps these cross-partition rules consistent.
func validateBoundary(operationID string, w http.ResponseWriter, r *http.Request) bool {
	if r == nil {
		return true
	}
	if values, present := r.URL.Query()["pageToken"]; present {
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_PAGE_TOKEN", "pageToken must not be empty", []apitransport.ProblemFieldError{{Field: "pageToken", Code: "INVALID_PAGE_TOKEN", Detail: "pageToken must not be empty"}})
				return false
			}
		}
	}
	if expected, checked := expectedRequestContentType(operationID, r.Method); checked {
		actual := strings.TrimSpace(r.Header.Get("Content-Type"))
		hasBody, err := requestHasBody(r)
		if err != nil {
			apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "The request body could not be read", nil)
			return false
		}
		if actual == "" && hasBody {
			apitransport.WriteProblem(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type is required for a non-empty request body.", nil)
			return false
		}
		if actual != "" {
			media, _, err := mime.ParseMediaType(actual)
			if err != nil || !strings.EqualFold(media, expected) {
				apitransport.WriteProblem(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Unsupported media type.", nil)
				return false
			}
		}
	}
	if isSemanticBoundaryOperation(operationID) {
		if !validateSemanticBody(w, r) {
			return false
		}
	}
	return true
}

// requestHasBody distinguishes an absent body from a chunked/unknown-length
// body without consuming data that the generated decoder still needs.
func requestHasBody(r *http.Request) (bool, error) {
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return false, nil
	}
	if r.ContentLength > 0 {
		return true, nil
	}
	buffered := bufio.NewReader(r.Body)
	original := r.Body
	r.Body = struct {
		io.Reader
		io.Closer
	}{Reader: buffered, Closer: original}
	_, err := buffered.Peek(1)
	if err == io.EOF {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func expectedRequestContentType(operationID, method string) (string, bool) {
	if operationID == "uploadCurrentAvatar" || operationID == "uploadProductLogo" {
		// Avatar uploads accept a small allowlist of image media types. The
		// access handler validates that allowlist and the decoded bytes.
		return "", false
	}
	switch operationID {
	case "uploadProjectCandidateSourceBlob":
		return "application/octet-stream", true
	}
	// All generated mutation/query bodies are JSON except the two raw blob
	// operations above. Checking only body-capable methods avoids rejecting a
	// harmless Content-Type header on GET/DELETE requests.
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return "application/json", true
	default:
		return "", false
	}
}

func isSemanticBoundaryOperation(operationID string) bool {
	switch operationID {
	case "querySemanticModel", "previewSemanticDataset", "explainSemanticModelQuery", "explainSemanticPreview":
		return true
	default:
		return false
	}
}

func validateSemanticBody(w http.ResponseWriter, r *http.Request) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxGeneratedJSONBodyBytes+1))
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "The request body could not be read", nil)
		return false
	}
	if int64(len(raw)) > maxGeneratedJSONBodyBytes {
		apitransport.WriteProblem(w, r, http.StatusRequestEntityTooLarge, "CONTENT_TOO_LARGE", "The request body exceeds the configured size limit", nil)
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		// The generated/domain decoder owns malformed JSON diagnostics. We only
		// inspect structurally valid bodies here.
		return true
	}
	limits := map[string]int{"dimensions": 50, "metrics": 50, "filters": 100, "sort": 50}
	for field, max := range limits {
		value, present := object[field]
		if !present || string(value) == "null" {
			continue
		}
		var items []json.RawMessage
		if err := json.Unmarshal(value, &items); err != nil {
			return semanticViolation(w, r, field, "must be an array")
		}
		if len(items) > max {
			return semanticViolation(w, r, field, fmt.Sprintf("must contain at most %d items", max))
		}
	}
	if value, present := object["limit"]; present && string(value) != "null" {
		var limit int64
		if err := json.Unmarshal(value, &limit); err != nil || limit < 1 || limit > 1000 {
			return semanticViolation(w, r, "limit", "must be between 1 and 1000")
		}
	}
	if value, present := object["pageToken"]; present && string(value) != "null" {
		var token string
		if err := json.Unmarshal(value, &token); err != nil || strings.TrimSpace(token) == "" || len(token) > 2048 {
			return semanticViolation(w, r, "pageToken", "must contain 1 to 2048 characters")
		}
	}
	return true
}

func semanticViolation(w http.ResponseWriter, r *http.Request, field, detail string) bool {
	apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST", detail, []apitransport.ProblemFieldError{{Field: field, Code: "INVALID_REQUEST", Detail: detail}})
	return false
}
