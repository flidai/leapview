package runtimefactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/candidatecatalog"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

// ExpectedRelations derives the physical relation contract from the compiled
// semantic models rather than from a hand-maintained table list.
func ExpectedRelations(artifacts release.CandidateArtifactSet) []candidatecatalog.LogicalRelation {
	seen := map[string]struct{}{}
	result := make([]candidatecatalog.LogicalRelation, 0)
	for _, model := range artifacts.Compiler.Artifact.Models() {
		if model == nil {
			continue
		}
		for table := range model.Tables {
			relation := candidatecatalog.LogicalRelation{Schema: "model", Table: table}
			key := relation.Schema + "\x00" + relation.Table
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, relation)
		}
	}
	return result
}

// VerifyExpectedRelations is the reuse-mode read-only relation assertion.
func VerifyExpectedRelations(actual []candidatecatalog.CatalogTable, expected []candidatecatalog.LogicalRelation) error {
	seen := make(map[string]struct{}, len(actual))
	for _, relation := range actual {
		seen[relation.Schema+"\x00"+relation.Table] = struct{}{}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("reused catalog relation count %d does not match compiled contract %d", len(seen), len(expected))
	}
	for _, relation := range expected {
		if _, ok := seen[relation.Schema+"\x00"+relation.Table]; !ok {
			return fmt.Errorf("reused catalog is missing compiled relation %s.%s", relation.Schema, relation.Table)
		}
	}
	return nil
}

func planDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func candidateDataConfigDigest(targetID, environment string, mode release.GenerationDataMode, revision string) string {
	return planDigest(strings.Join([]string{targetID, environment, string(mode), revision}, "\x00"))
}

// finalizeCandidatePlanExecution reconciles the hypothetical reuse contract
// used to evaluate exact physical identity with the execution the persisted
// plan will actually perform. A rejected reuse decision is a source refresh,
// so its config digest and operator-facing evidence must not continue to claim
// the retained snapshot even though that was the inspected starting point.
func finalizeCandidatePlanExecution(request deployment.DeliveryPlanRequest, candidateID string, artifacts release.CandidateArtifactSet) (deployment.DeliveryPlanRequest, error) {
	if artifacts.Generation.DataMode != release.GenerationDataReuseBase {
		return request, nil
	}
	decision, found := deployment.ResolveDeliveryReuseDecision(&deployment.DeliveryPlan{Evidence: request.Evidence}, candidateID)
	if found && decision.Reusable {
		return request, nil
	}
	revision, err := release.CandidateSourcesDataRevision(artifacts.Artifact.SourceDigest, artifacts.Generation.ManagedDataPins)
	if err != nil {
		return deployment.DeliveryPlanRequest{}, fmt.Errorf("derive effective source-refresh revision: %w", err)
	}
	request.Execution.ConfigDigest = candidateDataConfigDigest(request.TargetID, request.Environment, release.GenerationDataRefreshSources, revision)
	request.Evidence.Compatibility.SemanticChanges = append(request.Evidence.Compatibility.SemanticChanges, "effective data mode=refresh_sources")
	if !found || !decision.RetainBase {
		request.Evidence.PhysicalWorkStatement = "refreshes compiled project relations in a private DuckLake catalog"
		request.Evidence.ReuseStatement = "does not reuse the retained base because exact execution or physical identities changed"
	}
	return request, nil
}

func finalizeReuseEvidence(evidence *deployment.DeliveryPlanEvidence, decisions []deployment.DeliveryReuseDecision) {
	if evidence == nil || len(decisions) == 0 {
		return
	}
	allReusable := true
	anyRetained := false
	for _, decision := range decisions {
		allReusable = allReusable && decision.Reusable
		anyRetained = anyRetained || decision.Reusable || decision.RetainBase
		if !decision.Reusable {
			evidence.PhysicalWork.Refreshes = append(evidence.PhysicalWork.Refreshes, decision.ResourceID)
		}
	}
	if anyRetained && !allReusable {
		evidence.PhysicalWorkStatement = "retains unchanged sealed relations and rebuilds impacted relations in a private catalog"
		evidence.ReuseStatement = "retains exact sealed references only for relations with matching execution and physical identities"
	} else if allReusable {
		evidence.PhysicalWorkStatement = "verifies the retained sealed catalog without rewriting relations"
		evidence.ReuseStatement = "reuses exact retained relation references because all relation identities match"
	}
}

// pipelineScopeRelationIDs resolves the refresh compiler's authored model names
// through the project graph. Resource IDs are opaque and must never be inferred
// from a name by string convention.
func pipelineScopeRelationIDs(scope []string, graph projectgraph.ProjectGraph, relationExecution map[string]string) (map[string]string, error) {
	modelIDsByName := make(map[string]string)
	for _, resource := range graph.Resources() {
		if resource.Kind == projectgraph.KindModel {
			modelIDsByName[resource.Name] = resource.ID.String()
		}
	}
	resolved := make(map[string]string, len(scope))
	for _, selected := range scope {
		relationID := modelIDsByName[selected]
		if relationID == "" {
			return nil, fmt.Errorf("pipeline materialization scope relation %q is absent from the compiled model graph", selected)
		}
		if _, ok := relationExecution[relationID]; !ok {
			return nil, fmt.Errorf("pipeline materialization scope relation %q is absent from compiled relation evidence", selected)
		}
		resolved[relationID] = selected
	}
	return resolved, nil
}

// materializationIdentity is the execution identity for physical relations.
// The portable project artifact also contains dashboards, access, and other
// serving metadata; including its digest would force a full physical rebuild
// for a dashboard-only change. Model materialization descriptors are the narrow
// materialization contract, encoded canonically so unchanged relation inputs
// retain their sealed references while changed descriptors trigger a partial
// rebuild from that base.
func materializationIdentity(artifacts release.CandidateArtifactSet) (string, error) {
	artifact := artifacts.Compiler.Artifact
	manifest := artifact.Manifest()
	graph := artifact.Graph()
	physicalResources := make([]struct {
		ID   string            `json:"id"`
		Kind projectgraph.Kind `json:"kind"`
		Name string            `json:"name"`
	}, 0)
	physicalIDs := make(map[projectgraph.ResourceID]struct{})
	for _, resource := range graph.Resources() {
		switch resource.Kind {
		case projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel:
			physicalIDs[resource.ID] = struct{}{}
			physicalResources = append(physicalResources, struct {
				ID   string            `json:"id"`
				Kind projectgraph.Kind `json:"kind"`
				Name string            `json:"name"`
			}{ID: resource.ID.String(), Kind: resource.Kind, Name: resource.Name})
		}
	}
	sort.Slice(physicalResources, func(i, j int) bool { return physicalResources[i].ID < physicalResources[j].ID })
	physicalEdges := make([]struct {
		From     string `json:"from"`
		To       string `json:"to"`
		Relation string `json:"relation,omitempty"`
	}, 0)
	for _, edge := range graph.Edges() {
		if _, ok := physicalIDs[edge.From]; !ok {
			continue
		}
		if _, ok := physicalIDs[edge.To]; !ok {
			continue
		}
		physicalEdges = append(physicalEdges, struct {
			From     string `json:"from"`
			To       string `json:"to"`
			Relation string `json:"relation,omitempty"`
		}{From: edge.From.String(), To: edge.To.String(), Relation: edge.Relation})
	}
	sort.Slice(physicalEdges, func(i, j int) bool {
		if physicalEdges[i].From != physicalEdges[j].From {
			return physicalEdges[i].From < physicalEdges[j].From
		}
		if physicalEdges[i].To != physicalEdges[j].To {
			return physicalEdges[i].To < physicalEdges[j].To
		}
		return physicalEdges[i].Relation < physicalEdges[j].Relation
	})
	// Keep the complete physical projection here: source/model descriptors,
	// target-relevant connection semantics, and source/model dependency edges.
	// Dashboards, access policy, and other serving metadata are intentionally
	// excluded so they remain provenance/governance changes only.
	payload := struct {
		Connections any `json:"connections,omitempty"`
		Sources     any `json:"sources,omitempty"`
		Models      any `json:"models,omitempty"`
		Resources   any `json:"resources,omitempty"`
		Edges       any `json:"edges,omitempty"`
	}{Connections: manifest.Connections, Sources: manifest.Sources, Models: manifest.Models, Resources: physicalResources, Edges: physicalEdges}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode materialization identity: %w", err)
	}
	return planDigest(string(encoded)), nil
}

// MaterializationIdentity exposes the canonical physical-projection identity
// to the local candidate runner without exposing that runner's persistence
// adapters through this package.
func MaterializationIdentity(artifacts release.CandidateArtifactSet) (string, error) {
	return materializationIdentity(artifacts)
}

// CandidateDeliveryPolicy is resolved by the target owner at composition
// time. Planning must not invent approval, rollback, or retention claims.
type CandidateDeliveryPolicy struct {
	RequiresApproval       bool
	ApprovalPolicyRevision int64
	RollbackClass          deployment.DeliveryRollbackClass
	RetentionWindow        string
}

const CurrentApprovalPolicyRevision int64 = 1

func (p CandidateDeliveryPolicy) normalized() (CandidateDeliveryPolicy, error) {
	if p.ApprovalPolicyRevision < 1 {
		return CandidateDeliveryPolicy{}, fmt.Errorf("approval policy revision must be positive")
	}
	if p.RollbackClass == "" {
		p.RollbackClass = deployment.DeliveryServingSafe
	}
	if p.RollbackClass != deployment.DeliveryRollbackSafe && p.RollbackClass != deployment.DeliveryServingSafe && p.RollbackClass != deployment.DeliveryNonReversible {
		return CandidateDeliveryPolicy{}, fmt.Errorf("unsupported delivery rollback class %q", p.RollbackClass)
	}
	if strings.TrimSpace(p.RetentionWindow) != "" {
		if duration, err := time.ParseDuration(strings.TrimSpace(p.RetentionWindow)); err != nil || duration <= 0 {
			return CandidateDeliveryPolicy{}, fmt.Errorf("invalid delivery rollback retention window %q", p.RetentionWindow)
		}
		p.RetentionWindow = strings.TrimSpace(p.RetentionWindow)
	}
	return p, nil
}

// CandidatePlanRequest constructs the canonical target-specific plan from the
// compiler evidence retained in CandidateArtifactSet. It intentionally does
// not inspect a worktree, credentials, or physical storage.
func CandidatePlanRequest(input deployment.DeliveryCandidateBuildInput, artifacts release.CandidateArtifactSet, runtimeVersion string, now time.Time) (deployment.DeliveryPlanRequest, error) {
	return CandidatePlanRequestWithPolicyAndReuse(input, artifacts, runtimeVersion, CandidateDeliveryPolicy{ApprovalPolicyRevision: CurrentApprovalPolicyRevision}, now, nil)
}

func CandidatePlanRequestWithPolicy(input deployment.DeliveryCandidateBuildInput, artifacts release.CandidateArtifactSet, runtimeVersion string, policy CandidateDeliveryPolicy, now time.Time) (deployment.DeliveryPlanRequest, error) {
	return CandidatePlanRequestWithPolicyAndReuse(input, artifacts, runtimeVersion, policy, now, nil)
}

// CandidatePlanRequestWithPolicyAndReuse computes the canonical plan and, when
// supplied, records the target-resolved physical identity used for reuse. The
// target composition layer supplies this context from the active generation,
// candidate, and verified seal; the compiler never guesses catalog identity.
func CandidatePlanRequestWithPolicyAndReuse(input deployment.DeliveryCandidateBuildInput, artifacts release.CandidateArtifactSet, runtimeVersion string, policy CandidateDeliveryPolicy, now time.Time, reuse *deployment.DeliveryReuseInput) (deployment.DeliveryPlanRequest, error) {
	if input.Candidate.ID == "" || input.Candidate.TargetID == "" || input.ProjectID.Validate() != nil {
		return deployment.DeliveryPlanRequest{}, fmt.Errorf("candidate plan scope is incomplete")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if input.Plan != nil && !input.Plan.CreatedAt.IsZero() {
		now = input.Plan.CreatedAt.UTC()
	}
	if input.PipelinePlan != nil {
		pipelinePlan := input.PipelinePlan
		if err := pipelinePlan.Validate(); err != nil {
			return deployment.DeliveryPlanRequest{}, fmt.Errorf("pipeline plan: %w", err)
		}
		if pipelinePlan.ArtifactDigest != input.ArtifactDigest {
			return deployment.DeliveryPlanRequest{}, fmt.Errorf("pipeline plan artifact digest differs from delivery source")
		}
	}
	if strings.TrimSpace(runtimeVersion) == "" {
		return deployment.DeliveryPlanRequest{}, fmt.Errorf("candidate runtime version is required")
	}
	var policyErr error
	policy, policyErr = policy.normalized()
	if policyErr != nil {
		return deployment.DeliveryPlanRequest{}, policyErr
	}
	materializationDigest, digestErr := materializationIdentity(artifacts)
	if digestErr != nil {
		return deployment.DeliveryPlanRequest{}, digestErr
	}
	dataInputs := []deployment.DeliveryDataInput{{ID: "source-artifact", Mode: deployment.DeliveryDataPinned, Revision: materializationDigest}}
	for _, pin := range artifacts.Generation.ManagedDataPins {
		if strings.TrimSpace(pin.ConnectionID) == "" || strings.TrimSpace(pin.RevisionID) == "" {
			return deployment.DeliveryPlanRequest{}, fmt.Errorf("managed-data pin is incomplete")
		}
		dataInputs = append(dataInputs, deployment.DeliveryDataInput{ID: pin.ConnectionID, Mode: deployment.DeliveryDataPinned, Revision: pin.RevisionID})
	}
	// CompilerDigest is a physical execution context, not a whole-project
	// serving identity. The complete graph remains in qualification,
	// provenance, and serving-artifact evidence; relation descriptors and
	// materializationIdentity carry the physical projection below.
	compilerDigest := planDigest(artifacts.Artifact.CompilerVersion + "\x00" + strconv.Itoa(artifacts.Artifact.SchemaVersion))
	// Binding identity includes the effective connector requirements as well as
	// authorization. Secret material and provider versions are intentionally
	// absent, so credential rotation with unchanged endpoint semantics remains
	// physically reusable.
	// AuthorizationFingerprint is governance evidence, not connector binding
	// semantics. Keeping it out of BindingDigest lets policy-only changes
	// retain exact physical relation references while still changing the plan's
	// governance digests below.
	bindingParts := []string{}
	for _, requirement := range append([]release.CandidateConnectionRequirement(nil), artifacts.Generation.Connections...) {
		bindingParts = append(bindingParts, requirement.ConnectionID.String()+"\x00"+requirement.ConnectorKind+"\x00"+string(requirement.Access))
	}
	for _, authored := range append([]release.CandidateAuthoredConnection(nil), artifacts.Generation.AuthoredConnections...) {
		bindingParts = append(bindingParts, authored.ConnectionID.String()+"\x00"+authored.ConnectorKind+"\x00"+string(authored.Access))
	}
	sort.Strings(bindingParts)
	bindingDigest := planDigest(strings.Join(bindingParts, "\x00"))
	runtimeDigest := planDigest(runtimeVersion)
	// Data mode/revision are execution semantics: switching from a retained
	// snapshot to a source refresh must never retain a physical reuse key.
	// ConfigDigest remains a complete plan identity (including data mode and
	// revision). ContextDigest below deliberately excludes it when deciding
	// per-relation retention so one table refresh does not invalidate siblings.
	configDigest := candidateDataConfigDigest(input.Candidate.TargetID, input.Candidate.Scope.Environment, artifacts.Generation.DataMode, artifacts.Generation.DataRevision)
	// Planning is read-only and therefore has no serving-state/artifact row
	// yet. Bind qualification to retained compiler/contract evidence only;
	// materialized serving identities are checked and sealed during Build.
	qualificationDigest := planDigest(artifacts.Compiler.Graph.Digest() + "\x00" + artifacts.Artifact.ProjectDigest + "\x00" + artifacts.Artifact.CompilerVersion + "\x00" + strconv.Itoa(artifacts.Artifact.SchemaVersion))
	operation := input.Operation
	if operation == "" {
		operation = deployment.DeliveryOperationCodeChange
	}
	physicalStatement := "refreshes compiled project relations in a private DuckLake catalog"
	reuseStatement := "no physical relation reuse has been declared"
	if artifacts.Generation.DataMode == release.GenerationDataReuseBase {
		physicalStatement = "verifies the retained sealed snapshot without rewriting relations"
		reuseStatement = "reuses the exact retained base snapshot because compiler materialization impact is false"
	}
	impact := deployment.DeliveryGraphImpact{}
	for _, change := range artifacts.Compiler.Plan.Changes {
		item := deployment.DeliveryImpactResource{ID: change.ID, Kind: change.Type, Change: change.Action, Reason: change.Reason}
		switch change.Action {
		case "add":
			impact.Added = append(impact.Added, item)
		case "remove":
			impact.Removed = append(impact.Removed, item)
		default:
			impact.DirectlyModified = append(impact.DirectlyModified, item)
		}
	}
	for _, change := range artifacts.Compiler.Plan.DependencyChanges {
		impact.IndirectlyAffected = append(impact.IndirectlyAffected, deployment.DeliveryImpactResource{ID: change.To, Kind: change.ResourceKind, Change: change.Action, Reason: "compiler dependency impact", Paths: []string{change.From + " -> " + change.To}})
		impact.RelationshipPaths = append(impact.RelationshipPaths, change.From+" -> "+change.To)
	}
	evidence := deployment.DeliveryPlanEvidence{
		ImpactStatement:       fmt.Sprintf("compiler graph contains %d direct and %d dependency changes", len(artifacts.Compiler.Plan.Changes), len(artifacts.Compiler.Plan.DependencyChanges)),
		PhysicalWorkStatement: physicalStatement,
		ReuseStatement:        reuseStatement,
		GraphImpact:           impact,
		Compatibility:         deployment.DeliveryCompatibilityImpact{Breaking: artifacts.Compiler.Plan.Summary.Breaking, SemanticChanges: []string{fmt.Sprintf("materialization impact=%t", artifacts.Compiler.Plan.Summary.MaterializationImpact)}},
		PhysicalWork: deployment.DeliveryPhysicalWork{
			Materializations: []string{artifacts.Compiler.Plan.Project},
			Estimates:        []deployment.DeliveryEstimate{{Work: "candidate catalog", LowerBound: 1, UpperBound: float64(maxInt(1, len(artifacts.Compiler.Plan.Models))), Expected: float64(maxInt(1, len(artifacts.Compiler.Plan.Models))), Unit: "relation-set", Basis: "compiled semantic Model count", Confidence: "high"}},
		},
		Qualification: deployment.DeliveryQualificationEvidence{Policy: "target-owned exact schema closure and admitted compatibility; core object probes and read-only attach", Steps: qualificationSteps()},
		StalePolicy:   deployment.DeliveryStalePolicy{Mode: "reject", Description: "target revision or active base changes reject before physical work"},
		Rollback:      deployment.DeliveryRollbackEvidence{Class: policy.RollbackClass, RetentionWindow: policy.RetentionWindow, Description: "sealed catalog remains immutable; rollback class and retention are target policy"},
	}
	request := deployment.DeliveryPlanRequest{
		ID: "plan-" + input.Candidate.ID, ActorID: input.OwnerID, TargetID: input.Candidate.TargetID, ProjectID: input.ProjectID.String(), Environment: input.Candidate.Scope.Environment,
		Operation: operation, SourceDigest: input.ArtifactDigest,
		Execution:  deployment.DeliveryExecutionInputs{SourceArtifactDigest: input.ArtifactDigest, MaterializationDigest: materializationDigest, CompilerDigest: compilerDigest, ExecutableDigest: planDigest("leapview-executable:" + runtimeVersion), DependencyDigest: planDigest("leapview-dependencies:" + runtimeVersion), ConfigDigest: configDigest, BindingDigest: bindingDigest, RuntimeDigest: runtimeDigest, CapabilityDigest: bindingDigest, DataInputs: dataInputs},
		Provenance: deployment.DeliveryProvenance{Repository: sourceRepository(input), SourceRevision: sourceRevision(input), Builder: "leapview", BuildDefinition: artifacts.Artifact.CompilerVersion, AttestationDigest: input.Source.SourceAttestationDigest},
		Governance: deployment.DeliveryGovernance{PolicyDigest: artifacts.AuthorizationFingerprint, AuthorizationDigest: artifacts.AuthorizationFingerprint, QualificationDigest: qualificationDigest, ExpiresAt: func() time.Time {
			if input.Plan != nil && !input.Plan.Governance.ExpiresAt.IsZero() {
				return input.Plan.Governance.ExpiresAt
			}
			return now.Add(time.Hour)
		}(), RequiresApproval: policy.RequiresApproval, ApprovalPolicyRevision: policy.ApprovalPolicyRevision, ObservedInputsAllowed: false},
		Evidence:  evidence,
		CreatedAt: now, Persist: true,
	}
	if input.PipelinePlan != nil {
		pipelinePlan := *input.PipelinePlan
		request.PipelinePlan = &pipelinePlan
	}
	currentContextDigest, err := request.Execution.ContextDigest()
	if err != nil {
		return deployment.DeliveryPlanRequest{}, err
	}
	if reuse != nil || artifacts.Generation.DataMode == release.GenerationDataReuseBase {
		decision := deployment.DeliveryReuseDecision{ResourceID: input.Candidate.ID, Reusable: false, Reason: "retained base reuse identity is unavailable"}
		if operation != deployment.DeliveryOperationCodeChange && input.PipelinePlan != nil && reuse != nil && len(artifacts.Compiler.RelationExecution) > 0 {
			scopedRelationIDs, err := pipelineScopeRelationIDs(input.PipelinePlan.MaterializationScope, artifacts.Compiler.Artifact.Graph(), artifacts.Compiler.RelationExecution)
			if err != nil {
				return deployment.DeliveryPlanRequest{}, err
			}
			candidateReuse := *reuse
			// Canonical target composition must compare the current execution
			// context with the exact active-base context. Do not preserve an
			// omitted context for compatibility callers; EvaluateDeliveryReuse
			// rejects an incomplete identity before any relation can be reused.
			candidateReuse.ContextDigest = currentContextDigest
			candidateReuse.Deterministic = candidateReuse.Deterministic && artifacts.Generation.Deterministic
			if candidateReuse.EquivalenceToken == "" {
				candidateReuse.EquivalenceToken = artifacts.Generation.EquivalenceToken
			}
			for _, planned := range request.Execution.DataInputs {
				if planned.Mode == deployment.DeliveryDataObserved {
					candidateReuse.Observed = true
				}
			}
			ids := make([]string, 0, len(artifacts.Compiler.RelationExecution))
			for id := range artifacts.Compiler.RelationExecution {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			decisions := make([]deployment.DeliveryReuseDecision, 0, len(ids))
			for _, id := range ids {
				if _, scoped := scopedRelationIDs[id]; scoped {
					decisions = append(decisions, deployment.DeliveryReuseDecision{ResourceID: id, Reusable: false, RetainBase: true, Reason: "pipeline materialization scope requires refresh"})
					continue
				}
				baseDigest := artifacts.Compiler.BaseRelationExecution[id]
				if baseDigest == "" {
					return deployment.DeliveryPlanRequest{}, fmt.Errorf("pipeline relation %q is outside materialization scope and has no exact base execution identity", id)
				}
				relationReuse := candidateReuse
				relationReuse.RelationScoped = true
				relationReuse.ResourceID = id
				relationReuse.ExecutionDigest = artifacts.Compiler.RelationExecution[id]
				relationReuse.BaseExecutionDigest = baseDigest
				// An unselected relation is not re-executed by a pipeline refresh.
				// It retains the exact sealed base reference after the execution,
				// catalog, pool, compatibility, and context identities below match.
				// Project-wide observed/nondeterministic execution flags therefore
				// apply only to the selected materialization work, not this retained
				// immutable relation.
				relationReuse.Deterministic = true
				relationReuse.Observed = false
				decision, reuseErr := deployment.EvaluateDeliveryReuse(relationReuse)
				if reuseErr != nil {
					return deployment.DeliveryPlanRequest{}, fmt.Errorf("evaluate scoped relation reuse %q: %w", id, reuseErr)
				}
				if !decision.Reusable {
					return deployment.DeliveryPlanRequest{}, fmt.Errorf("pipeline relation %q is outside materialization scope and cannot be reused exactly: %s", id, decision.Reason)
				}
				decisions = append(decisions, decision)
			}
			request.Evidence.Reuse = decisions
			finalizeReuseEvidence(&request.Evidence, decisions)
			return finalizeCandidatePlanExecution(request, input.Candidate.ID, artifacts)
		} else if operation != deployment.DeliveryOperationCodeChange {
			decision.Reason = "operation requires explicit full materialization"
		} else if reuse != nil && len(artifacts.Compiler.RelationExecution) > 0 {
			candidateReuse := *reuse
			// Canonical target composition supplies the active base context;
			// omitted context identities must fail closed rather than selecting
			// a compatibility path for candidate-level callers.
			candidateReuse.ContextDigest = currentContextDigest
			candidateReuse.Deterministic = candidateReuse.Deterministic && artifacts.Generation.Deterministic
			if candidateReuse.EquivalenceToken == "" {
				candidateReuse.EquivalenceToken = artifacts.Generation.EquivalenceToken
			}
			for _, planned := range request.Execution.DataInputs {
				if planned.Mode == deployment.DeliveryDataObserved {
					candidateReuse.Observed = true
					break
				}
			}
			if len(artifacts.Compiler.RelationExecution) > 0 {
				ids := make([]string, 0, len(artifacts.Compiler.RelationExecution))
				for id := range artifacts.Compiler.RelationExecution {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				decisions := make([]deployment.DeliveryReuseDecision, 0, len(ids))
				for _, id := range ids {
					baseDigest := artifacts.Compiler.BaseRelationExecution[id]
					if baseDigest == "" {
						decisions = append(decisions, deployment.DeliveryReuseDecision{ResourceID: id, Reason: "base relation execution identity is unavailable"})
						continue
					}
					relationReuse := candidateReuse
					relationReuse.RelationScoped = true
					relationReuse.ResourceID = id
					relationReuse.ExecutionDigest = artifacts.Compiler.RelationExecution[id]
					relationReuse.BaseExecutionDigest = baseDigest
					relationDecision, reuseErr := deployment.EvaluateDeliveryReuse(relationReuse)
					if reuseErr != nil {
						return deployment.DeliveryPlanRequest{}, fmt.Errorf("evaluate candidate relation reuse %q: %w", id, reuseErr)
					}
					decisions = append(decisions, relationDecision)
				}
				request.Evidence.Reuse = decisions
				finalizeReuseEvidence(&request.Evidence, decisions)
				return finalizeCandidatePlanExecution(request, input.Candidate.ID, artifacts)
			}
		}
		if operation != deployment.DeliveryOperationCodeChange {
			// Restatements and binding/policy operations are never allowed to
			// inherit a base through a candidate-wide identity match.
		} else if artifacts.Generation.DataMode != release.GenerationDataReuseBase {
			decision.Reason = "refresh mode requires private materialization"
		} else if reuse != nil {
			candidateReuse := *reuse
			// Canonical reuse always compares exact current/base execution
			// context identities. An omitted base context is incomplete and is
			// rejected by EvaluateDeliveryReuse.
			candidateReuse.ContextDigest = currentContextDigest
			candidateReuse.Deterministic = candidateReuse.Deterministic && artifacts.Generation.Deterministic
			if candidateReuse.EquivalenceToken == "" {
				candidateReuse.EquivalenceToken = artifacts.Generation.EquivalenceToken
			}
			for _, planned := range request.Execution.DataInputs {
				if planned.Mode == deployment.DeliveryDataObserved {
					candidateReuse.Observed = true
					break
				}
			}
			candidateReuse.ResourceID = input.Candidate.ID
			executionDigest, digestErr := request.Execution.ExecutionDigest()
			if digestErr != nil {
				return deployment.DeliveryPlanRequest{}, digestErr
			}
			candidateReuse.ExecutionDigest = executionDigest
			var reuseErr error
			decision, reuseErr = deployment.EvaluateDeliveryReuse(candidateReuse)
			if reuseErr != nil {
				return deployment.DeliveryPlanRequest{}, fmt.Errorf("evaluate candidate reuse: %w", reuseErr)
			}
		}
		request.Evidence.Reuse = []deployment.DeliveryReuseDecision{decision}
		finalizeReuseEvidence(&request.Evidence, request.Evidence.Reuse)
	}
	return finalizeCandidatePlanExecution(request, input.Candidate.ID, artifacts)
}

// QualificationRequestForCandidate declares only checks with independent
// evidence. candidatecatalog itself performs object probes, snapshot
// normalization, and read-only attach; this policy adds exact relation
// closure and compatibility admission. Publication approval and live access
// authorization remain separate target-owned boundaries.
func QualificationRequestForCandidate(artifacts release.CandidateArtifactSet) candidatecatalog.QualificationRequest {
	expected := candidatecatalog.QualificationExpectations{}
	for _, model := range artifacts.Compiler.Artifact.Models() {
		if model == nil {
			continue
		}
		for table := range model.Tables {
			expected.Relations = append(expected.Relations, candidatecatalog.LogicalRelation{Schema: "model", Table: table})
		}
	}
	return candidatecatalog.QualificationRequest{
		CatalogID:            artifacts.Generation.Identity.GenerationID,
		Expected:             expected,
		PolicyDigest:         artifacts.AuthorizationFingerprint,
		ReviewerPolicyDigest: artifacts.AuthorizationFingerprint,
		Policy: func(_ context.Context, input candidatecatalog.QualificationInput) error {
			if err := VerifyExpectedRelations(input.Record.Closure.Tables, input.Expectations.Relations); err != nil {
				return err
			}
			if err := input.Record.Compatibility.Validate(); err != nil {
				return fmt.Errorf("admitted compatibility evidence: %w", err)
			}
			if strings.TrimSpace(input.Record.Closure.Digest) == "" {
				return fmt.Errorf("normalized closure digest is missing")
			}
			return nil
		},
	}
}

func qualificationSteps() []deployment.DeliveryQualificationStep {
	return []deployment.DeliveryQualificationStep{{ID: "schema-closure", Kind: "schema", Description: "compare the exact normalized relation closure with compiled models; core probes every physical object and read-only attaches one current snapshot", Required: true, Blocking: true}, {ID: "compatibility", Kind: "contract", Description: "verify the catalog carries the admitted physical-pool compatibility tuple", Required: true, Blocking: true}}
}

func sourceRepository(input deployment.DeliveryCandidateBuildInput) string {
	if input.Source.SourceRevision != nil {
		return strings.TrimSpace(input.Source.SourceRevision.Repository)
	}
	return ""
}

func sourceRevision(input deployment.DeliveryCandidateBuildInput) string {
	if input.Source.SourceRevision != nil {
		return strings.TrimSpace(input.Source.SourceRevision.Revision)
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
