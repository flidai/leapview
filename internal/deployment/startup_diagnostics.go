package deployment

import (
	"errors"
	"strings"
)

// ErrDeliveryStartupNotReady is returned when a production target cannot
// prove that its sealed delivery contract is administrable and safe to serve.
// The error deliberately carries only stable diagnostic codes; storage paths,
// credentials, and object keys never belong in startup output.
var ErrDeliveryStartupNotReady = errors.New("delivery startup is not ready")

type DeliveryStartupDiagnosticCode string

const (
	DeliveryStartupMissingPoolAdmission     DeliveryStartupDiagnosticCode = "missing_physical_pool_admission"
	DeliveryStartupUnadmittedPool           DeliveryStartupDiagnosticCode = "physical_pool_not_admitted"
	DeliveryStartupLegacyServingIdentity    DeliveryStartupDiagnosticCode = "migrated_serving_state_identity_missing"
	DeliveryStartupMixedServingPaths        DeliveryStartupDiagnosticCode = "mixed_legacy_and_sealed_serving_paths"
	DeliveryStartupMissingTargetRevision    DeliveryStartupDiagnosticCode = "target_revision_missing"
	DeliveryStartupMissingServingGeneration DeliveryStartupDiagnosticCode = "active_serving_generation_missing"
	DeliveryStartupIndeterminatePublication DeliveryStartupDiagnosticCode = "indeterminate_publication_state"
)

// DeliveryStartupDiagnostic is a stable, non-secret readiness reason. Scope
// is a coarse control-plane scope (for example target or project), never a
// raw filesystem path, object key, or credential value.
type DeliveryStartupDiagnostic struct {
	Code  DeliveryStartupDiagnosticCode `json:"code"`
	Scope string                        `json:"scope,omitempty"`
}

// DeliveryStartupDiagnosticsError preserves all independent startup failures
// so operators can repair one migration/bootstrap issue per restart rather
// than discovering them one at a time. errors.Is(err,
// ErrDeliveryStartupNotReady) remains stable for health wiring.
type DeliveryStartupDiagnosticsError struct {
	Diagnostics []DeliveryStartupDiagnostic
}

func (e *DeliveryStartupDiagnosticsError) Error() string {
	if e == nil || len(e.Diagnostics) == 0 {
		return ErrDeliveryStartupNotReady.Error()
	}
	parts := make([]string, 0, len(e.Diagnostics))
	for _, diagnostic := range e.Diagnostics {
		if diagnostic.Code == "" {
			continue
		}
		parts = append(parts, string(diagnostic.Code))
	}
	if len(parts) == 0 {
		return ErrDeliveryStartupNotReady.Error()
	}
	return ErrDeliveryStartupNotReady.Error() + ": " + strings.Join(parts, ",")
}

func (e *DeliveryStartupDiagnosticsError) Unwrap() error { return ErrDeliveryStartupNotReady }

func DeliveryStartupDiagnosticsOf(err error) []DeliveryStartupDiagnostic {
	var diagnostics *DeliveryStartupDiagnosticsError
	if !errors.As(err, &diagnostics) || diagnostics == nil {
		return nil
	}
	return append([]DeliveryStartupDiagnostic(nil), diagnostics.Diagnostics...)
}

// DeliveryStartupState is the small evidence seam used by process startup and
// readiness. The caller obtains these values from the migrated control store;
// this domain validator never opens SQLite or infers an admission from config.
type DeliveryStartupState struct {
	Production                   bool
	TargetID                     string
	ProjectID                    string
	Environment                  string
	ConfiguredPhysicalPoolID     string
	PhysicalPoolExists           bool
	PhysicalPoolAdmitted         bool
	TargetRevisionExists         bool
	ActiveServingGeneration      bool
	ActiveServingStateIdentity   string
	MigratedRowsWithoutServingID int
	LegacyServingPathEnabled     bool
	IndeterminatePublication     bool
}

// ValidateDeliveryStartup enforces the controlled rollout boundary. A fresh
// production target may be administrable without a pool, but it cannot become
// ready for delivery or serving until an operator explicitly creates and
// admits one. Development/evaluation targets retain their separate local
// serving contract and therefore do not require a physical-pool admission.
func ValidateDeliveryStartup(state DeliveryStartupState) error {
	if !state.Production {
		return nil
	}
	diagnostics := make([]DeliveryStartupDiagnostic, 0, 4)
	scope := strings.TrimSpace(state.TargetID)
	if scope == "" {
		scope = strings.TrimSpace(state.ProjectID)
	}
	appendDiagnostic := func(code DeliveryStartupDiagnosticCode) {
		for _, existing := range diagnostics {
			if existing.Code == code && existing.Scope == scope {
				return
			}
		}
		diagnostics = append(diagnostics, DeliveryStartupDiagnostic{Code: code, Scope: scope})
	}
	if strings.TrimSpace(state.ConfiguredPhysicalPoolID) == "" {
		appendDiagnostic(DeliveryStartupMissingPoolAdmission)
	} else if !state.PhysicalPoolExists || !state.PhysicalPoolAdmitted {
		code := DeliveryStartupUnadmittedPool
		if !state.PhysicalPoolExists {
			code = DeliveryStartupMissingPoolAdmission
		}
		appendDiagnostic(code)
	}
	if !state.TargetRevisionExists {
		appendDiagnostic(DeliveryStartupMissingTargetRevision)
	}
	// A publication whose external activation outcome is unknown is never
	// retried or treated as committed during startup. Reconciliation must first
	// prove the target CAS and generation identity from durable control state.
	if state.IndeterminatePublication {
		appendDiagnostic(DeliveryStartupIndeterminatePublication)
	}
	if state.MigratedRowsWithoutServingID > 0 {
		appendDiagnostic(DeliveryStartupLegacyServingIdentity)
	}
	if state.LegacyServingPathEnabled {
		appendDiagnostic(DeliveryStartupMixedServingPaths)
	}
	if state.ActiveServingGeneration && strings.TrimSpace(state.ActiveServingStateIdentity) == "" {
		appendDiagnostic(DeliveryStartupLegacyServingIdentity)
	}
	if len(diagnostics) == 0 {
		return nil
	}
	return &DeliveryStartupDiagnosticsError{Diagnostics: diagnostics}
}
