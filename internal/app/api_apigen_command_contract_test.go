package app

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
)

func TestAPIGenOperationKindsAndRoleMappingAreExhaustive(t *testing.T) {
	rolesByCapability := make(map[access.Capability][]string)
	for _, role := range access.CanonicalProjectRoles() {
		for _, capability := range access.ProjectRoleCapabilities(role) {
			rolesByCapability[capability] = append(rolesByCapability[capability], string(role))
		}
	}
	runtimeContracts := apiaggregate.GetAPIGenCommandRuntimeContracts()
	commandCount := 0
	for operationID, contract := range apiaggregate.GetAPIGenOperationContracts() {
		switch contract.Kind {
		case apiaggregate.GenOperationKindQuery:
			if contract.Command != nil {
				t.Errorf("query %s has command metadata: %#v", operationID, contract.Command)
			}
		case apiaggregate.GenOperationKindCommand:
			commandCount++
			command := contract.Command
			if command == nil {
				t.Errorf("command %s has no command metadata", operationID)
				continue
			}
			if !command.Audit.Required || command.Audit.SuccessAction == "" {
				t.Errorf("command %s does not require a stable success audit: %#v", operationID, command.Audit)
			}
			if command.Audit.Payload == nil {
				t.Errorf("command %s has no typed audit payload contract", operationID)
			}
			if command.Failures == nil {
				t.Errorf("command %s did not explicitly declare its failure vocabulary", operationID)
			}
			runtimeFailures, ok := apiaggregate.GetAPIGenCommandFailureContracts(operationID)
			if !ok {
				t.Errorf("command %s has no generated runtime failure contract", operationID)
			} else if len(runtimeFailures) != len(command.Failures) {
				t.Errorf("command %s runtime failure count = %d, generated count = %d", operationID, len(runtimeFailures), len(command.Failures))
			} else {
				for index, failure := range command.Failures {
					runtimeFailure := runtimeFailures[index]
					if runtimeFailure.Kind != failure.Kind || runtimeFailure.StatusCode != failure.StatusCode || runtimeFailure.Code != failure.Code || runtimeFailure.PublicDetail != failure.PublicDetail {
						t.Errorf("command %s runtime failure %#v differs from generated failure %#v", operationID, runtimeFailure, failure)
					}
					if !slices.Contains(contract.DocumentedStatusCodes, failure.StatusCode) {
						t.Errorf("command %s failure %q status %d is not documented", operationID, failure.Kind, failure.StatusCode)
					}
				}
			}
			if command.Audit.Guarantee != "transactional" && command.Audit.Guarantee != "best-effort" {
				t.Errorf("command %s has no supported audit guarantee: %#v", operationID, command.Audit)
			}
			runtimeContract, ok := apiaggregate.GetAPIGenCommandRuntimeContract(operationID)
			if !ok {
				t.Errorf("command %s has no generated runtime contract", operationID)
			} else if err := runtimeContract.Validate(); err != nil {
				t.Errorf("command %s runtime contract is invalid: %v", operationID, err)
			} else if runtimeContract.OperationID != operationID || runtimeContract.Owner != command.Owner ||
				runtimeContract.Method != contract.Method || runtimeContract.Path != contract.Path ||
				string(runtimeContract.Idempotency) != command.Idempotency || string(runtimeContract.Concurrency) != command.Concurrency ||
				runtimeContract.AuthzMode != command.AuthzMode || runtimeContract.Privilege != command.Privilege ||
				runtimeContract.AuditAction != command.Audit.SuccessAction || string(runtimeContract.Guarantee) != command.Audit.Guarantee {
				t.Errorf("command %s runtime contract %#v differs from generated metadata %#v", operationID, runtimeContract, command)
			} else if command.Audit.Payload == nil || runtimeContract.AuditPayload == nil {
				t.Errorf("command %s runtime audit payload is missing: generated=%#v runtime=%#v", operationID, command.Audit.Payload, runtimeContract.AuditPayload)
			} else if runtimeContract.AuditPayload.Schema != command.Audit.Payload.Schema ||
				runtimeContract.AuditPayload.SchemaVersion != command.Audit.Payload.SchemaVersion ||
				string(runtimeContract.AuditPayload.Retention) != command.Audit.Payload.Retention ||
				len(runtimeContract.AuditPayload.Fields) != len(command.Audit.Payload.Fields) {
				t.Errorf("command %s runtime audit payload %#v differs from generated metadata %#v", operationID, runtimeContract.AuditPayload, command.Audit.Payload)
			} else {
				for index, field := range command.Audit.Payload.Fields {
					runtimeField := runtimeContract.AuditPayload.Fields[index]
					if runtimeField.Name != field.Name || string(runtimeField.Sensitivity) != field.Sensitivity {
						t.Errorf("command %s runtime audit field %#v differs from generated field %#v", operationID, runtimeField, field)
					}
				}
			}
			if ok {
				if (command.Target == nil) != (runtimeContract.Target == nil) {
					t.Errorf("command %s runtime target %#v differs from generated target %#v", operationID, runtimeContract.Target, command.Target)
				} else if command.Target != nil && (runtimeContract.Target.Parameter != command.Target.Parameter || runtimeContract.Target.Type != command.Target.Type) {
					t.Errorf("command %s runtime target %#v differs from generated target %#v", operationID, runtimeContract.Target, command.Target)
				}
				if len(runtimeContract.AdditionalExposures) != len(command.AdditionalExposures) {
					t.Errorf("command %s runtime exposures %#v differ from generated exposures %#v", operationID, runtimeContract.AdditionalExposures, command.AdditionalExposures)
				} else {
					for index, exposure := range command.AdditionalExposures {
						if string(runtimeContract.AdditionalExposures[index]) != string(exposure) {
							t.Errorf("command %s runtime exposure %q differs from generated exposure %q", operationID, runtimeContract.AdditionalExposures[index], exposure)
						}
					}
				}
				dependencies := runtimeContract.Dependencies()
				for dependency, required := range map[apigencommand.Dependency]bool{
					apigencommand.DependencyAuthorization: command.AuthzMode != "none",
					apigencommand.DependencyIdempotency:   command.Idempotency == "required",
					apigencommand.DependencyConcurrency:   command.Concurrency == "if-match",
					apigencommand.DependencyAudit:         true,
					apigencommand.DependencyJobQueue:      command.Execution != nil,
				} {
					if slices.Contains(dependencies, dependency) != required {
						t.Errorf("command %s dependency %q required=%v dependencies=%#v", operationID, dependency, required, dependencies)
					}
				}
				if runtimeContract.SpanName() != "command."+operationID {
					t.Errorf("command %s span name = %q", operationID, runtimeContract.SpanName())
				}
			}
			if command.AuthzMode != contract.AuthzMode {
				t.Errorf("command %s authz mode %q differs from operation mode %q", operationID, command.AuthzMode, contract.AuthzMode)
			}
			if contract.Method == http.MethodPost {
				if operationID == "saveCredentialDraft" || operationID == "validateCredentialDraft" {
					if command.Idempotency != "forbidden" || !contract.RequestBodyRequired || !command.Audit.Required || command.Audit.Guarantee != "transactional" || command.UI != nil || len(command.AdditionalExposures) != 0 {
						t.Errorf("non-replayable credential draft POST must have a required body, forbidden idempotency, transactional audit, and no additional exposure: operation=%#v command=%#v", contract, command)
					}
				} else if command.Idempotency != "required" {
					t.Errorf("POST command %s idempotency = %q", operationID, command.Idempotency)
				}
			}
			if contract.Method == http.MethodPatch && command.Concurrency != "if-match" {
				t.Errorf("PATCH command %s concurrency = %q", operationID, command.Concurrency)
			}
			if command.Target != nil && !strings.Contains(contract.Path, "{"+command.Target.Parameter+"}") {
				t.Errorf("command %s target %#v is absent from %s", operationID, command.Target, contract.Path)
			}
			if command.AuthzMode == "privilege" {
				capability, err := access.ParseCapability(command.Privilege)
				if err != nil {
					t.Errorf("command %s has unknown capability %q", operationID, command.Privilege)
					continue
				}
				if capability == access.CapabilityPlatformAdmin {
					if len(rolesByCapability[capability]) != 0 {
						t.Errorf("platform capability %q must not be granted by a project role", capability)
					}
					continue
				}
				if len(rolesByCapability[capability]) == 0 {
					t.Errorf("command %s capability %q is not granted by any project role", operationID, capability)
				}
			}
		default:
			t.Errorf("operation %s has no normalized command/query kind: %q", operationID, contract.Kind)
		}
	}
	if len(runtimeContracts) != commandCount {
		t.Errorf("runtime command registry has %d entries, want %d generated commands", len(runtimeContracts), commandCount)
	}
}
