package module

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/publication"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var errPublicationCommandAuditUnavailable = apigenfailure.New("audit_unavailable", "dashboard publication command audit is unavailable")

type publicationCommandAuditInput struct {
	operationID    string
	projectID      projectgraph.ResourceID
	principalID    string
	targetID       string
	requestID      string
	correlationID  string
	surface        string
	idempotencyKey string
	aggregateKey   string
}

func validatePublicationCommandAuditContracts() error {
	operationIDs := []string{
		dashboardgen.GenOperationSuspendDashboardPublication,
		dashboardgen.GenOperationResumeDashboardPublication,
		dashboardgen.GenOperationRotateDashboardPublication,
	}
	for _, operationID := range operationIDs {
		generated, ok := dashboardgen.GetAPIGenOperationContract(operationID)
		if !ok || generated.Command == nil {
			return fmt.Errorf("dashboard publication operation %q is missing its generated command contract", operationID)
		}
		command := generated.Command
		if command.AuthzMode != "authenticated" || generated.AuthzMode != command.AuthzMode ||
			!command.Audit.Required || command.Audit.SuccessAction == "" || command.Target == nil ||
			command.Audit.Guarantee != "transactional" || command.Target.Type != "project" || command.Target.Parameter != "project" {
			return fmt.Errorf("dashboard publication operation %q has an invalid generated command audit contract", operationID)
		}
	}
	return nil
}

// buildPublicationAuditIntent translates the generated command contract into
// Access' durable, non-secret handoff. Event identity is derived from the
// operation, stable publication aggregate, and caller idempotency key so a
// retried command is idempotent at the outbox boundary.
func buildPublicationAuditIntent(input publicationCommandAuditInput) (access.AuditIntent, error) {
	generated, ok := dashboardgen.GetAPIGenOperationContract(input.operationID)
	if !ok || generated.Command == nil {
		return access.AuditIntent{}, fmt.Errorf("dashboard publication operation %q has no command audit contract", input.operationID)
	}
	command := generated.Command
	if command.Audit.Guarantee != "transactional" {
		return access.AuditIntent{}, fmt.Errorf("dashboard publication operation %q does not provide transactional auditing", input.operationID)
	}
	payload := dashboardgen.GenSchemaDashboardPublicationCommandAuditPayload{
		OperationId: input.operationID, Owner: command.Owner, Surface: input.surface,
	}
	var metadata string
	var err error
	switch input.operationID {
	case string(dashboardgen.GenOperationSuspendDashboardPublication):
		metadata, err = dashboardgen.EncodeGenSuspendDashboardPublicationAuditPayload(payload)
	case string(dashboardgen.GenOperationResumeDashboardPublication):
		metadata, err = dashboardgen.EncodeGenResumeDashboardPublicationAuditPayload(payload)
	case string(dashboardgen.GenOperationRotateDashboardPublication):
		metadata, err = dashboardgen.EncodeGenRotateDashboardPublicationAuditPayload(payload)
	default:
		return access.AuditIntent{}, fmt.Errorf("dashboard publication operation %q has no audit payload encoder", input.operationID)
	}
	if err != nil {
		return access.AuditIntent{}, err
	}
	metadata, err = canonicalPublicationAuditMetadata(metadata)
	if err != nil {
		return access.AuditIntent{}, err
	}
	aggregateKey := strings.TrimSpace(input.aggregateKey)
	if aggregateKey == "" {
		aggregateKey = "dashboard_publication:" + input.projectID.String() + ":" + strings.TrimSpace(input.targetID)
	}
	idempotencyKey := strings.TrimSpace(input.idempotencyKey)
	if idempotencyKey == "" {
		return access.AuditIntent{}, fmt.Errorf("dashboard publication audit intent requires idempotency key")
	}
	// The caller's idempotency key is the stable request identity for this
	// durable event. Transport request/correlation IDs may legitimately change
	// when a response is lost and the same command is replayed.
	requestID := idempotencyKey
	sum := sha256.Sum256([]byte("dashboard-publication\x00" + input.operationID + "\x00" + aggregateKey + "\x00" + idempotencyKey))
	intent := access.AuditIntent{
		EventID: "dashboard-publication:" + hex.EncodeToString(sum[:16]),
		Source:  "dashboard.publication", Operation: input.operationID,
		PrincipalID: strings.TrimSpace(input.principalID), Action: command.Audit.SuccessAction,
		ResourceKind: "project", ResourceID: input.projectID.String(), Capability: access.CapabilityResourcePublish,
		Outcome: "success", RequestID: requestID, CorrelationID: requestID,
		AggregateKey: aggregateKey, MetadataJSON: metadata,
	}
	canonical, err := intent.Canonicalize()
	if err != nil {
		return access.AuditIntent{}, err
	}
	return canonical, nil
}

func publicationOperationID(action publication.Action) (dashboardgen.GenCommandOperationID, bool) {
	switch action {
	case publication.ActionSuspend:
		return dashboardgen.GenCommandOperationSuspendDashboardPublication(), true
	case publication.ActionResume:
		return dashboardgen.GenCommandOperationResumeDashboardPublication(), true
	case publication.ActionRotate:
		return dashboardgen.GenCommandOperationRotateDashboardPublication(), true
	default:
		return dashboardgen.GenCommandOperationID{}, false
	}
}

func publicationAuditRequestInput(r *http.Request, operationID string, projectID projectgraph.ResourceID, principalID, targetID string) publicationCommandAuditInput {
	requestID := firstPublicationHeader(r, "X-Request-Id", "X-Request-ID")
	correlationID := firstPublicationHeader(r, "X-Correlation-Id", "X-Correlation-ID")
	if correlationID == "" {
		correlationID = requestID
	}
	surface := "api"
	if strings.EqualFold(firstPublicationHeader(r, "X-LeapView-Invocation-Surface", "X-LeapView-Client"), "cli") {
		surface = "cli"
	}
	return publicationCommandAuditInput{
		operationID: operationID, projectID: projectID, principalID: strings.TrimSpace(principalID),
		targetID: strings.TrimSpace(targetID), requestID: requestID, correlationID: correlationID, surface: surface,
		idempotencyKey: firstPublicationHeader(r, "Idempotency-Key"),
	}
}

func canonicalPublicationAuditMetadata(raw string) (string, error) {
	var value map[string]any
	if err := json.Unmarshal([]byte(raw), &value); err != nil || value == nil {
		if err == nil {
			err = fmt.Errorf("metadata must be a JSON object")
		}
		return "", fmt.Errorf("dashboard publication audit metadata: %w", err)
	}
	// The generated payload deliberately contains only operation, owner, and
	// surface. Reject accidental secret-bearing fields if that schema expands.
	for _, key := range []string{"authorization", "bearer_token", "cookie", "password", "secret", "access_token", "query_text", "raw_sql"} {
		if _, found := value[key]; found {
			return "", fmt.Errorf("dashboard publication audit metadata contains forbidden field %q", key)
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("dashboard publication audit metadata: %w", err)
	}
	return string(encoded), nil
}

func firstPublicationHeader(r *http.Request, names ...string) string {
	if r == nil {
		return ""
	}
	for _, name := range names {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}
