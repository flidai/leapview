package module

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

// Operation coverage statuses are deliberately small and stable: consumers
// can distinguish an operation that has an exact typed contract from one that
// is still on bounded legacy compatibility, without interpreting an empty
// action as support.
const (
	APIGenOperationSupported   = "supported"
	APIGenOperationLegacyOnly  = "legacy-only"
	APIGenOperationUnsupported = "unsupported"
	APIGenOperationPublic      = "public"

	APIGenOperationQualified   = "qualified"
	APIGenOperationMapped      = "mapped-pending-qualification"
	APIGenOperationLegacy      = "legacy-compatibility"
	APIGenOperationUnqualified = "unqualified"
)

// APIGenOperationCoverage is the reviewable projection of one generated
// APIGen contract. It intentionally includes command ownership and exposed
// surfaces: authorization coverage is not complete if a route is known but
// its UI/agent/automation entry point is not accounted for.
type APIGenOperationCoverage struct {
	OperationID        string   `json:"operation"`
	Kind               string   `json:"kind,omitempty"`
	Namespace          string   `json:"namespace,omitempty"`
	Method             string   `json:"method"`
	Path               string   `json:"path"`
	TypedAction        string   `json:"typedAction,omitempty"`
	Resolver           string   `json:"resolver,omitempty"`
	LegacyMode         string   `json:"legacyMode"`
	ObjectScope        string   `json:"objectScope,omitempty"`
	EntryPoints        []string `json:"entryPoints,omitempty"`
	CommandOwner       string   `json:"commandOwner,omitempty"`
	CommandTarget      string   `json:"commandTarget,omitempty"`
	CommandIdempotency string   `json:"commandIdempotency,omitempty"`
	CommandConcurrency string   `json:"commandConcurrency,omitempty"`
	Dependencies       []string `json:"dependencies,omitempty"`
	Evidence           []string `json:"evidence,omitempty"`
	SupportStatus      string   `json:"supportStatus"`
	Qualification      string   `json:"qualificationStatus"`
	Reason             string   `json:"reason,omitempty"`
}

// APIGenOperationCoverageMatrix is deterministic by operation ID, making it
// suitable for a generated documentation artifact and a cheap stale-contract
// check in unit tests.
type APIGenOperationCoverageMatrix struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Operations    []APIGenOperationCoverage `json:"operations"`
}

// BuildAPIGenOperationCoverage mechanically projects generated APIGen
// contracts into the operation matrix. Validation errors are retained on the
// row as unsupported reasons so the artifact remains honest and useful during
// migration instead of hiding unmapped operations.
func BuildAPIGenOperationCoverage(operations map[string]APIGenOperationContract) APIGenOperationCoverageMatrix {
	ids := make([]string, 0, len(operations))
	for operationID := range operations {
		ids = append(ids, operationID)
	}
	sort.Strings(ids)
	rows := make([]APIGenOperationCoverage, 0, len(ids))
	for _, operationID := range ids {
		contract := operations[operationID]
		row := projectOperationCoverage(operationID, contract)
		rows = append(rows, row)
	}
	return APIGenOperationCoverageMatrix{SchemaVersion: 1, Operations: rows}
}

// ValidateAPIGenOperationCoverage checks the matrix's structural invariants.
// It does not require every route to have migrated: legacy-only is an
// explicit, bounded status and is returned to callers for prioritization.
func ValidateAPIGenOperationCoverage(matrix APIGenOperationCoverageMatrix) error {
	if matrix.SchemaVersion != 1 {
		return fmt.Errorf("unsupported operation coverage schema version %d", matrix.SchemaVersion)
	}
	seen := make(map[string]struct{}, len(matrix.Operations))
	for _, row := range matrix.Operations {
		if strings.TrimSpace(row.OperationID) == "" {
			return fmt.Errorf("operation coverage row has no operation ID")
		}
		if _, exists := seen[row.OperationID]; exists {
			return fmt.Errorf("duplicate operation coverage row %q", row.OperationID)
		}
		seen[row.OperationID] = struct{}{}
		if strings.TrimSpace(row.Method) == "" || strings.TrimSpace(row.Path) == "" {
			return fmt.Errorf("operation coverage row %q has incomplete route identity", row.OperationID)
		}
		if row.SupportStatus == APIGenOperationUnsupported && strings.TrimSpace(row.Reason) == "" {
			return fmt.Errorf("unsupported operation %q has no reason", row.OperationID)
		}
		if row.TypedAction == "" && row.Resolver != "" || row.TypedAction != "" && row.Resolver == "" {
			return fmt.Errorf("operation %q has partial typed metadata", row.OperationID)
		}
		if row.TypedAction != "" && row.SupportStatus == APIGenOperationSupported && row.Qualification != APIGenOperationQualified && row.Qualification != APIGenOperationMapped {
			return fmt.Errorf("typed operation %q has invalid qualification status %q", row.OperationID, row.Qualification)
		}
	}
	return nil
}

func projectOperationCoverage(operationID string, contract APIGenOperationContract) APIGenOperationCoverage {
	row := APIGenOperationCoverage{
		OperationID:   operationID,
		Kind:          contract.Kind,
		Namespace:     contract.Namespace,
		Method:        contract.Method,
		Path:          contract.Path,
		TypedAction:   strings.TrimSpace(contract.Action),
		Resolver:      strings.TrimSpace(contract.Resolver),
		LegacyMode:    "none",
		EntryPoints:   []string{"http"},
		SupportStatus: APIGenOperationPublic,
		Qualification: APIGenOperationQualified,
	}
	if scope, ok := apiGenScope(contract); ok {
		row.ObjectScope = scope
	} else {
		row.ObjectScope = "invalid"
	}
	if contract.Command != nil {
		row.CommandOwner = contract.Command.Owner
		row.CommandIdempotency = contract.Command.Idempotency
		row.CommandConcurrency = contract.Command.Concurrency
		if contract.Command.Target != nil {
			row.CommandTarget = contract.Command.Target.Type
		}
		for _, exposure := range contract.Command.AdditionalExposures {
			row.EntryPoints = appendUnique(row.EntryPoints, exposure)
		}
		if contract.Command.UIActionID != "" {
			row.EntryPoints = appendUnique(row.EntryPoints, "ui")
		}
	}
	sort.Strings(row.EntryPoints)

	hasAction := row.TypedAction != ""
	hasResolver := row.Resolver != ""
	switch {
	case hasAction != hasResolver:
		row.LegacyMode = "ambiguous"
		row.SupportStatus = APIGenOperationUnsupported
		row.Qualification = APIGenOperationUnqualified
		row.Reason = "typed action and resolver must be declared together"
	case hasAction:
		row.LegacyMode = "typed-attenuated"
		service := access.NewTypedOperationRequirementService()
		requirement, err := service.Requirement(access.Action(row.TypedAction), row.Resolver)
		if err != nil {
			row.SupportStatus = APIGenOperationUnsupported
			row.Qualification = APIGenOperationUnqualified
			row.Reason = err.Error()
		} else if err := validateTypedOperationScope(contract, requirement); err != nil {
			row.SupportStatus = APIGenOperationUnsupported
			row.Qualification = APIGenOperationUnqualified
			row.Reason = err.Error()
		} else {
			row.SupportStatus = APIGenOperationSupported
			row.Dependencies = typedOperationDependencies(requirement.Action)
			row.Evidence = append([]string(nil), apigenTypedQualificationEvidence[operationID]...)
			if len(row.Evidence) > 0 {
				row.Qualification = APIGenOperationQualified
			} else {
				row.Qualification = APIGenOperationMapped
			}
		}
	case contract.Protected && contract.AuthzMode == "privilege":
		row.LegacyMode = "capability"
		row.SupportStatus = APIGenOperationLegacyOnly
		row.Qualification = APIGenOperationLegacy
		row.Reason = "no typed action/resolver metadata; legacy capability is bounded compatibility"
	case contract.Protected:
		row.LegacyMode = "authenticated"
		row.SupportStatus = APIGenOperationLegacyOnly
		row.Qualification = APIGenOperationLegacy
		row.Reason = "authenticated operation has no typed action/resolver metadata"
	}
	return row
}

// Qualification evidence is intentionally explicit and reviewable. Adding a
// resolver/action pair proves only structural mapping; named focused tests or
// equivalent acceptance evidence are required before a row is qualified.
var apigenTypedQualificationEvidence = map[string][]string{
	"getDashboard": {"internal/access/module/apigen_typed_dashboard_test.go:TestAPIGenTypedDashboardReadRequiresExactPermissionPair"},
}

func typedOperationDependencies(action access.Action) []string {
	definition, ok := access.Permission(action)
	if !ok || len(definition.Prerequisites) == 0 {
		return nil
	}
	result := make([]string, 0, len(definition.Prerequisites))
	for _, prerequisite := range definition.Prerequisites {
		result = append(result, string(prerequisite))
	}
	sort.Strings(result)
	return result
}

func validateTypedOperationScope(contract APIGenOperationContract, requirement access.TypedOperationRequirement) error {
	scope, ok := apiGenScope(contract)
	if !ok || scope == "" {
		return fmt.Errorf("typed operation has malformed or missing object scope")
	}
	want := ""
	switch requirement.Resolver {
	case access.TypedOperationResolverDashboard:
		want = "dashboard"
	case access.TypedOperationResolverSemanticModel:
		want = "semantic-model"
	case access.TypedOperationResolverConnection:
		want = "connection"
	case access.TypedOperationResolverSource:
		want = "source"
	case access.TypedOperationResolverModel:
		want = "model"
	case access.TypedOperationResolverPipeline:
		want = "pipeline"
	case access.TypedOperationResolverResourceShare:
		want = "resource-share"
	case access.TypedOperationResolverProject:
		want = "project"
	case access.TypedOperationResolverDelivery:
		want = "delivery"
	case access.TypedOperationResolverInstance:
		want = "instance"
	}
	if want == "" || scope != want {
		return fmt.Errorf("typed resolver %q requires object scope %q, got %q", requirement.Resolver, want, scope)
	}
	return nil
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
