package authz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Principal struct {
	ID        string
	DevBypass bool
}

type Options struct {
	SnapshotFromContext   func(context.Context) (accesssnapshot.AuthorizationSnapshot, error)
	SubjectsFromContext   func(context.Context, string) ([]access.SubjectRef, error)
	PrincipalFromContext  func(context.Context) (Principal, bool)
	CredentialFromContext func(context.Context) (access.APICredential, bool)
	AuditRecorder         access.CanonicalAuditRecorder
}

type Metrics struct {
	queryruntime.Metrics
	snapshotFromContext   func(context.Context) (accesssnapshot.AuthorizationSnapshot, error)
	subjectsFromContext   func(context.Context, string) ([]access.SubjectRef, error)
	principalFromContext  func(context.Context) (Principal, bool)
	credentialFromContext func(context.Context) (access.APICredential, bool)
	auditRecorder         access.CanonicalAuditRecorder
}

// Planner forwards the activation-owned planner exposed by the active runtime.
// Authorization uses the same compiled semantic graph as execution and
// dashboard optimization; it never compiles a request-local planner.
func (m Metrics) Planner(modelID string) (consumer.Planner, bool) {
	provider, ok := m.Metrics.(interface {
		Planner(string) (consumer.Planner, bool)
	})
	if !ok {
		return nil, false
	}
	planner, available := provider.Planner(modelID)
	return planner, available && planner != nil
}

func (m Metrics) concretePlanner(modelID string) (*semanticquery.Planner, bool) {
	value, ok := m.Planner(modelID)
	planner, ok := value.(*semanticquery.Planner)
	return planner, ok && planner != nil
}

type DeniedError struct {
	PrincipalID string
	Capability  access.Capability
	Credential  bool
}

func (e DeniedError) Error() string {
	if e.Credential {
		return fmt.Sprintf("data query credential lacks %s", e.Capability)
	}
	return fmt.Sprintf("principal %q lacks %s on data object", e.PrincipalID, e.Capability)
}

func IsDenied(err error) bool {
	var denied DeniedError
	return errors.As(err, &denied)
}

func New(metrics queryruntime.Metrics, options Options) Metrics {
	return Metrics{
		Metrics:               metrics,
		snapshotFromContext:   options.SnapshotFromContext,
		subjectsFromContext:   options.SubjectsFromContext,
		principalFromContext:  options.PrincipalFromContext,
		credentialFromContext: options.CredentialFromContext,
		auditRecorder:         options.AuditRecorder,
	}
}

func (m Metrics) MetricsForProject(projectID projectgraph.ResourceID) (queryruntime.Metrics, bool) {
	if projectID == "" {
		return nil, false
	}
	provider, ok := m.Metrics.(queryruntime.ProjectMetrics)
	if ok {
		metrics, ok := provider.MetricsForProject(projectID)
		if !ok || metrics == nil {
			return nil, ok
		}
		m.Metrics = metrics
		return m, true
	}
	if m.Metrics == nil {
		return nil, false
	}
	catalog := m.Metrics.Catalog()
	if catalog.Project.ID == projectID {
		return m, true
	}
	return nil, false
}

func (m Metrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	if m.Metrics == nil {
		return dataquery.Result{}, errors.New("query metrics are not configured")
	}
	if m.snapshotFromContext == nil {
		return m.Metrics.ExecuteDataQuery(ctx, request)
	}
	governed, transform, err := m.GovernDataQuery(ctx, request)
	if err != nil {
		return rejectedDataQueryResult(err)
	}
	ctx = dataquery.WithGovernanceApplied(ctx)
	result, err := m.Metrics.ExecuteDataQuery(ctx, governed)
	if transform != nil {
		if transformErr := transform(&result, err); transformErr != nil {
			return rejectedDataQueryResult(transformErr)
		}
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func (m Metrics) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	if m.Metrics == nil {
		return dataquery.Result{}, errors.New("query metrics are not configured")
	}
	executor, ok := m.Metrics.(arrowquery.Executor)
	if !ok {
		return dataquery.Result{}, errors.New("query metrics do not support native Arrow execution")
	}
	if m.snapshotFromContext == nil {
		return executor.ExecuteDataQueryArrow(ctx, request, sink)
	}
	governed, transform, err := m.GovernDataQuery(ctx, request)
	if err != nil {
		return rejectedDataQueryResult(err)
	}
	_, publicationQuery := dashboardPublicationCapabilityFromContext(ctx)
	_, candidateQuery := candidateQueryCapabilityFromContext(ctx)
	_, viewAsQuery := viewAsCapabilityFromContext(ctx)
	durableStreamingAudit := publicationQuery || candidateQuery || viewAsQuery
	if durableStreamingAudit {
		// Arrow transports release records as the executor runs, so persist a
		// durable access identity before the sink can write its schema. The
		// completion event below enriches the audit trail with the final outcome; a
		// sustained completion-write failure is logged by PersistAuditEvent, but
		// cannot retroactively turn an already delivered stream into a rejection.
		capability := dataQueryCapability(governed)
		if candidateQuery {
			capability = access.CapabilityResourceRead
		}
		if err := m.recordDataAccessAudit(ctx, governed, capability, "started", nil); err != nil {
			return rejectedDataQueryResult(err)
		}
	}
	ctx = dataquery.WithGovernanceApplied(ctx)
	result, err := executor.ExecuteDataQueryArrow(ctx, governed, sink)
	if transform != nil {
		if transformErr := transform(&result, err); transformErr != nil {
			if durableStreamingAudit {
				return result, err
			}
			return rejectedDataQueryResult(transformErr)
		}
	}
	return result, err
}

func (m Metrics) GovernDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Query, dataquery.ResultTransformer, error) {
	request = request.WithMetadata(dataquery.MetadataFromContext(ctx))
	if err := request.ProjectID.Validate(); err != nil || strings.TrimSpace(request.ProjectID.String()) != request.ProjectID.String() {
		return request, nil, errors.New("project ID is required")
	}
	snapshot, err := m.authorizationSnapshot(ctx, request.ProjectID)
	if err != nil {
		return request, nil, err
	}
	resourceIndex, err := newProjectResourceIndex(snapshot.Project())
	if err != nil {
		return request, nil, err
	}
	candidateCapability, candidateQuery := candidateQueryCapabilityFromContext(ctx)
	capabilityAction := dataQueryCapability(request)
	if candidateQuery {
		// Candidate execution is always preview, even when it renders a normal
		// dashboard interaction whose production equivalent uses RESOURCE_USE.
		capabilityAction = access.CapabilityResourceRead
	}
	objects := m.dataQueryObjects(resourceIndex, request)
	capability, publicationQuery := dashboardPublicationCapabilityFromContext(ctx)
	viewAs, viewAsQuery := viewAsCapabilityFromContext(ctx)
	semanticObjects, physicalObjects, err := m.resolvedDependencyObjects(resourceIndex, request, publicationQuery)
	if err != nil {
		return request, nil, err
	}
	objects = append(objects, semanticObjects...)
	objects = append(objects, physicalObjects...)
	if publicationQuery {
		if err := validateDashboardPublicationCapability(snapshot.Project(), capability); err != nil {
			if auditErr := m.recordDataAccessAudit(ctx, request, access.CapabilityResourceRead, "denied", err); auditErr != nil {
				return request, nil, errors.Join(err, auditErr)
			}
			return request, nil, err
		}
		if candidateQuery || viewAsQuery || request.CandidateID != "" {
			request.PrincipalID = dashboardPublicationSubjectID(capability.ProjectID, capability.Publication)
			err := errors.New("public query cannot use candidate or view-as authority")
			if auditErr := m.recordDataAccessAudit(ctx, request, access.CapabilityResourceRead, "denied", err); auditErr != nil {
				return request, nil, errors.Join(err, auditErr)
			}
			return request, nil, err
		}
		objects = append(objects, m.dataQueryColumnObjects(resourceIndex, request)...)
		request.PrincipalID = dashboardPublicationSubjectID(capability.ProjectID, capability.Publication)
		if err := validateDashboardPublicationQuery(capability, request, objects); err != nil {
			if auditErr := m.recordDataAccessAudit(ctx, request, access.CapabilityResourceRead, "denied", err); auditErr != nil {
				return request, nil, errors.Join(err, auditErr)
			}
			return request, nil, err
		}
		governed, policies, err := m.applyDataPolicies(ctx, request, objects, resourceIndex)
		if err != nil {
			if auditErr := m.recordDataAccessAudit(ctx, request, access.CapabilityResourceRead, "error", err); auditErr != nil {
				return request, nil, errors.Join(err, auditErr)
			}
			return request, nil, err
		}
		governed.EffectivePolicyFingerprint = effectivePolicyFingerprint(
			governed,
			access.CapabilityResourceRead,
			objects,
			policies,
			effectivePolicyContext{},
		)
		return governed, func(result *dataquery.Result, executeErr error) error {
			status := "success"
			if executeErr != nil || (result != nil && result.Status == dataquery.StatusError) {
				status = "error"
			}
			return m.recordDataAccessAudit(ctx, governed, access.CapabilityResourceRead, status, executeErr)
		}, nil
	}
	principalID := strings.TrimSpace(request.PrincipalID)
	principal, authenticated := m.currentPrincipal(ctx)
	if authenticated {
		if principalID != "" && principalID != principal.ID {
			request.PrincipalID = principal.ID
			err := DeniedError{PrincipalID: principal.ID, Capability: capabilityAction}
			_ = m.recordDataAccessAudit(ctx, request, capabilityAction, "denied", err)
			return request, nil, err
		}
		if !candidateQuery && request.CandidateID != "" {
			request.PrincipalID = principal.ID
			err := DeniedError{PrincipalID: principal.ID, Capability: capabilityAction}
			_ = m.recordDataAccessAudit(ctx, request, capabilityAction, "denied", err)
			return request, nil, err
		}
		if candidateQuery {
			var err error
			request, err = validateCandidateQueryCapability(candidateCapability, principal, request)
			if err != nil {
				denied := DeniedError{PrincipalID: principal.ID, Capability: capabilityAction}
				_ = m.recordDataAccessAudit(ctx, request, capabilityAction, "denied", err)
				return request, nil, denied
			}
			principalID = request.PrincipalID
		}
		if viewAsQuery {
			var err error
			request, err = m.authorizeViewAs(ctx, principal, request, viewAs)
			if err != nil {
				return request, nil, err
			}
			principalID = request.PrincipalID
		}
		if principal.DevBypass && !candidateQuery && !viewAsQuery {
			request.PrincipalID = principal.ID
			request.EffectivePolicyFingerprint = effectivePolicyFingerprint(
				request,
				capabilityAction,
				objects,
				nil,
				effectivePolicyContext{
					Mode:             "dev_bypass",
					CredentialID:     currentCredentialID(ctx, m),
					ActorPrincipalID: principal.ID,
				},
			)
			return request, nil, nil
		}
		if principalID == "" {
			principalID = principal.ID
			request.PrincipalID = principal.ID
		}
	}
	if candidateQuery && !authenticated {
		err := dataquery.ErrMissingPrincipal
		_ = m.recordDataAccessAudit(ctx, request, capabilityAction, "denied", err)
		return request, nil, err
	}
	if viewAsQuery && !authenticated {
		err := dataquery.ErrMissingPrincipal
		_ = m.recordDataAccessAudit(ctx, request, capabilityAction, "denied", err)
		return request, nil, err
	}
	if principalID == "" {
		err := dataquery.ErrMissingPrincipal
		_ = m.recordDataAccessAudit(ctx, request, capabilityAction, "denied", err)
		return request, nil, err
	}
	if credential, ok := m.currentCredential(ctx); ok && !m.tokenAllowsCapability(ctx, snapshot, principalID, credential.Token, capabilityAction) {
		err := DeniedError{PrincipalID: principalID, Capability: capabilityAction, Credential: true}
		_ = m.recordDataAccessAudit(ctx, request, capabilityAction, "denied", err)
		return request, nil, err
	}
	bootstrapCandidateOwner := candidateQuery && candidateCapability.BootstrapAuthorized &&
		!viewAsQuery && request.PrincipalID == candidateCapability.OwnerPrincipalID
	if !bootstrapCandidateOwner {
		if ok, err := m.authorizeDataQuery(ctx, snapshot, principalID, capabilityAction, request, objects); err != nil {
			_ = m.recordDataAccessAudit(ctx, request, capabilityAction, "error", err)
			return request, nil, err
		} else if !ok {
			err := DeniedError{PrincipalID: principalID, Capability: capabilityAction}
			_ = m.recordDataAccessAudit(ctx, request, capabilityAction, "denied", err)
			return request, nil, err
		}
	}
	governed, policies, err := m.applyDataPolicies(ctx, request, objects, resourceIndex)
	if err != nil {
		_ = m.recordDataAccessAudit(ctx, request, capabilityAction, "error", err)
		return request, nil, err
	}
	candidateDigest := ""
	if candidateQuery {
		candidateDigest = candidateCapability.PolicyDigest
	}
	governed.EffectivePolicyFingerprint = effectivePolicyFingerprint(
		governed,
		capabilityAction,
		objects,
		policies,
		effectivePolicyContext{
			CandidateDigest:  candidateDigest,
			CredentialID:     currentCredentialID(ctx, m),
			ActorPrincipalID: principal.ID,
		},
	)
	strictAudit := candidateQuery || viewAsQuery
	return governed, func(result *dataquery.Result, executeErr error) error {
		if executeErr != nil {
			auditErr := m.recordDataAccessAudit(ctx, governed, capabilityAction, "error", executeErr)
			if strictAudit && auditErr != nil {
				return errors.Join(executeErr, auditErr)
			}
			return nil
		}
		status := "success"
		if result != nil && result.Status == dataquery.StatusError {
			status = "error"
		}
		auditErr := m.recordDataAccessAudit(ctx, governed, capabilityAction, status, nil)
		if strictAudit {
			return auditErr
		}
		return nil
	}, nil
}

func (m Metrics) authorizeDataQuery(ctx context.Context, snapshot accesssnapshot.AuthorizationSnapshot, principalID string, capability access.Capability, request dataquery.Query, objects []access.ResourceRef) (bool, error) {
	if err := capability.Validate(); err != nil {
		return false, err
	}
	subjects, err := m.subjects(ctx, principalID)
	if err != nil {
		return false, err
	}
	allows := func(resource access.ResourceRef) (bool, error) {
		for _, subject := range subjects {
			ok, err := snapshot.Allows(subject, resource, capability)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}
	if request.Kind == dataquery.KindSemanticAggregate && request.Target == "" {
		for _, object := range objects {
			if object.Kind() != projectgraph.KindSemanticModel {
				continue
			}
			ok, err := allows(object)
			if err != nil || !ok {
				return ok, err
			}
		}
		return true, nil
	}
	// A direct semantic/physical grant is sufficient for governed execution;
	// when a query has only physical dependencies, every dependency must be
	// authorized. This preserves the old semantic-or-physical closure without
	// reducing authorization to a project-wide read check.
	for _, object := range objects {
		if object.Kind() == projectgraph.KindSemanticModel || object.Kind() == projectgraph.KindModel {
			ok, err := allows(object)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
	}
	physical := make([]access.ResourceRef, 0, len(objects))
	for _, object := range objects {
		if object.Kind() == projectgraph.KindSource || object.Kind() == projectgraph.KindModel {
			physical = append(physical, object)
		}
	}
	if len(physical) == 0 {
		return false, nil
	}
	for _, object := range physical {
		ok, err := allows(object)
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

func (m Metrics) resolvedDependencyObjects(resourceIndex projectResourceIndex, request dataquery.Query, includePublicInteractions bool) ([]access.ResourceRef, []access.ResourceRef, error) {
	switch request.Kind {
	case dataquery.KindSemanticAggregate, dataquery.KindSemanticSpatialTile, dataquery.KindSemanticSpatialTileBudget, dataquery.KindSemanticSpatialMetadata:
	case dataquery.KindSemanticRows, dataquery.KindSemanticHistogram, dataquery.KindSemanticDistribution:
		if !includePublicInteractions {
			return nil, nil, nil
		}
	default:
		return nil, nil, nil
	}
	model, ok := m.Metrics.SemanticModel(request.ModelID)
	if !ok || model == nil {
		return nil, nil, fmt.Errorf("unknown semantic model %q", request.ModelID)
	}
	dimensions := dataFieldsToSemanticFields(request.Fields)
	metrics := dataFieldsToSemanticFields(request.Metrics)
	for _, field := range request.AuthorizationFields {
		if semanticFieldIsMetric(model, field.Field) {
			metrics = append(metrics, semanticquery.Field{Field: field.Field, Alias: field.Alias})
		} else {
			dimensions = append(dimensions, semanticquery.Field{Field: field.Field, Alias: field.Alias})
		}
	}
	if request.Value.Field != "" {
		field := semanticquery.Field{Field: request.Value.Field, Alias: request.Value.Alias}
		if semanticFieldIsMetric(model, request.Value.Field) {
			metrics = append(metrics, field)
		} else {
			dimensions = append(dimensions, field)
		}
	}
	queryRequest := semanticquery.Request{
		Dataset:    request.Target,
		Dimensions: dimensions,
		Metrics:    metrics,
		Time:       semanticquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
		Filters:    dataFiltersToSemanticFilters(request.Filters),
		Sort:       dataSortToSemanticSort(request.Sort),
	}
	planner, ok := m.concretePlanner(request.ModelID)
	if !ok {
		return nil, nil, fmt.Errorf("compiled semantic planner for model %q is unavailable", request.ModelID)
	}
	dependencies, err := planner.ResolveDependencies(queryRequest)
	if err != nil {
		return nil, nil, err
	}
	modelObject, ok := resourceIndex.byID(request.ModelID, projectgraph.KindSemanticModel)
	if !ok {
		return nil, nil, fmt.Errorf("unknown semantic model resource %q", request.ModelID)
	}
	semanticObjects := make([]access.ResourceRef, 0, len(dependencies.LogicalFields))
	for _, field := range dependencies.LogicalFields {
		if !isSemanticField(model, field) {
			continue
		}
		semanticObjects = append(semanticObjects, modelObject)
	}
	// Dependency cardinality is compiler-controlled, but do not combine two
	// independently sized slices into an allocation hint: the addition can
	// overflow before make applies its own bounds check. Appends retain the
	// same bounded result without an attacker-controlled capacity calculation.
	physicalObjects := make([]access.ResourceRef, 0)
	datasets := map[string]access.ResourceRef{}
	for _, datasetName := range dependencies.Datasets {
		dataset, ok := resourceIndex.byName(datasetName, projectgraph.KindModel)
		if !ok {
			continue
		}
		datasets[datasetName] = dataset
		physicalObjects = append(physicalObjects, dataset)
	}
	for _, field := range dependencies.PhysicalFields {
		table, column, ok := splitFieldRef(field)
		if !ok {
			continue
		}
		tableObject, ok := datasets[table]
		if !ok {
			tableObject, ok = resourceIndex.byName(table, projectgraph.KindModel)
			if !ok {
				continue
			}
			datasets[table] = tableObject
			physicalObjects = append(physicalObjects, tableObject)
		}
		// Graph resources intentionally stop at model/source granularity. A
		// physical column is authorized through its canonical model resource.
		_ = column
	}
	return semanticObjects, physicalObjects, nil
}

func semanticFieldIsMetric(model *semanticmodel.Model, field string) bool {
	if model == nil {
		return false
	}
	if _, ok := model.Metrics[field]; ok {
		return true
	}
	_, ok := model.Metrics[field]
	return ok
}

func isSemanticField(model *semanticmodel.Model, field string) bool {
	if _, ok := model.Dimensions[field]; ok {
		return true
	}
	if _, ok := model.Metrics[field]; ok {
		return true
	}
	_, ok := model.Metrics[field]
	return ok
}

func dataFieldsToSemanticFields(fields []dataquery.Field) []semanticquery.Field {
	out := make([]semanticquery.Field, 0, len(fields))
	for _, field := range fields {
		out = append(out, semanticquery.Field{Field: field.Field, Alias: field.Alias})
	}
	return out
}

func dataFiltersToSemanticFilters(filters []dataquery.Filter) []semanticquery.Filter {
	out := make([]semanticquery.Filter, 0, len(filters))
	for _, filter := range filters {
		groups := make([]semanticquery.FilterGroup, 0, len(filter.Groups))
		for _, group := range filter.Groups {
			groups = append(groups, semanticquery.FilterGroup{Filters: dataFiltersToSemanticFilters(group.Filters)})
		}
		out = append(out, semanticquery.Filter{Field: filter.Field, Dataset: filter.Dataset, Operator: filter.Operator, Values: append([]any{}, filter.Values...), Groups: groups, Spatial: dataSpatialFilterToSemantic(filter.Spatial)})
	}
	return out
}

func dataSpatialFilterToSemantic(value *dataquery.SpatialFilter) *semanticquery.SpatialFilter {
	if value == nil {
		return nil
	}
	points := make([]semanticquery.SpatialPoint, len(value.Points))
	for index, point := range value.Points {
		points[index] = semanticquery.SpatialPoint{Longitude: point.Longitude, Latitude: point.Latitude}
	}
	return &semanticquery.SpatialFilter{
		Kind: value.Kind, LatitudeField: value.LatitudeField, LongitudeField: value.LongitudeField, Dataset: value.Dataset,
		West: value.West, South: value.South, East: value.East, North: value.North, Points: points,
		Center: semanticquery.SpatialPoint{Longitude: value.Center.Longitude, Latitude: value.Center.Latitude}, RadiusMeters: value.RadiusMeters,
	}
}

func dataSortToSemanticSort(sort []dataquery.Sort) []semanticquery.Sort {
	out := make([]semanticquery.Sort, 0, len(sort))
	for _, item := range sort {
		out = append(out, semanticquery.Sort{Field: item.Field, Direction: item.Direction})
	}
	return out
}

func (m Metrics) recordDataAccessAudit(ctx context.Context, request dataquery.Query, capability access.Capability, status string, cause error) error {
	if m.auditRecorder == nil {
		return nil
	}
	action := "data_query.executed"
	if capability == access.CapabilityResourceRead {
		action = "data_preview.executed"
	}
	metadata := map[string]any{
		"candidateId":                request.CandidateID,
		"effectivePolicyFingerprint": request.EffectivePolicyFingerprint,
		"kind":                       string(request.Kind),
		"surface":                    request.Surface,
		"operation":                  request.Operation,
		"modelId":                    request.ModelID,
		"target":                     request.Target,
	}
	if cause != nil {
		metadata["error"] = cause.Error()
	}
	bytes, _ := json.Marshal(metadata)
	snapshot, err := m.authorizationSnapshot(ctx, request.ProjectID)
	if err != nil {
		return err
	}
	resource, ok := canonicalResourceByID(snapshot.Project(), request.ModelID, projectgraph.KindSemanticModel)
	if !ok {
		resource, err = access.NewResourceRef(request.ProjectID, projectgraph.KindProject)
		if err != nil {
			return err
		}
	}
	return m.persistCanonicalAudit(ctx, snapshot, access.CanonicalAuditEvent{Identity: snapshot.Identity(), PrincipalID: request.PrincipalID, Action: action,
		Resource: resource, Capability: capability, Status: status, RequestID: request.RequestID, CorrelationID: request.CorrelationID, MetadataJSON: string(bytes)})
}

func (m Metrics) QuerySemantic(ctx context.Context, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	query := semanticAggregateDataQuery(modelID, request)
	query.ProjectID = m.Metrics.Catalog().Project.ID
	result, err := m.ExecuteDataQuery(ctx, query)
	return queryRowsFromDataResult(result.Rows), err
}

func (m Metrics) PreviewSemantic(ctx context.Context, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	query := semanticRowsDataQuery(modelID, request)
	query.ProjectID = m.Metrics.Catalog().Project.ID
	result, err := m.ExecuteDataQuery(ctx, query)
	return queryRowsFromDataResult(result.Rows), err
}

func semanticAggregateDataQuery(modelID string, request reportdef.AggregateQuery) dataquery.Query {
	return dataquery.Query{
		ModelID: modelID,
		Kind:    dataquery.KindSemanticAggregate,
		Target:  request.Dataset,
		Fields:  queryFieldsToDataFields(request.Dimensions),
		Metrics: queryFieldsToDataFields(request.Metrics),
		Time:    dataquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
		Filters: queryFiltersToDataFilters(request.Filters),
		Sort:    querySortToDataSort(request.Sort),
		Limit:   request.Limit,
		Offset:  request.Offset,
	}
}

func semanticRowsDataQuery(modelID string, request reportdef.RowQuery) dataquery.Query {
	return dataquery.Query{
		ModelID: modelID,
		Kind:    dataquery.KindSemanticRows,
		Target:  request.Dataset,
		Fields:  queryFieldsToDataFields(request.Dimensions),
		Metrics: queryFieldsToDataFields(request.Metrics),
		Filters: queryFiltersToDataFilters(request.Filters),
		Sort:    querySortToDataSort(request.Sort),
		Limit:   request.Limit,
		Offset:  request.Offset,
	}
}

func queryFieldsToDataFields(fields []reportdef.QueryField) []dataquery.Field {
	out := make([]dataquery.Field, 0, len(fields))
	for _, field := range fields {
		out = append(out, dataquery.Field{
			Field: field.Field,
			Alias: field.Alias,
		})
	}
	return out
}

func queryFiltersToDataFilters(filters []reportdef.QueryFilter) []dataquery.Filter {
	out := make([]dataquery.Filter, 0, len(filters))
	for _, filter := range filters {
		groups := make([]dataquery.FilterGroup, 0, len(filter.Groups))
		for _, group := range filter.Groups {
			groups = append(groups, dataquery.FilterGroup{Filters: queryFiltersToDataFilters(group.Filters)})
		}
		out = append(out, dataquery.Filter{
			Field:    filter.Field,
			Dataset:  filter.Dataset,
			Operator: filter.Operator,
			Values:   append([]any{}, filter.Values...),
			Groups:   groups,
		})
	}
	return out
}

func querySortToDataSort(sort []reportdef.QuerySort) []dataquery.Sort {
	out := make([]dataquery.Sort, 0, len(sort))
	for _, item := range sort {
		out = append(out, dataquery.Sort{Field: item.Field, Direction: item.Direction})
	}
	return out
}

func queryRowsFromDataResult(rows []dataquery.Row) reportdef.QueryRows {
	out := make(reportdef.QueryRows, 0, len(rows))
	for _, row := range rows {
		converted := reportdef.QueryRow{}
		for key, value := range row {
			converted[key] = value
		}
		out = append(out, converted)
	}
	return out
}

func (m Metrics) QueryDashboard(ctx context.Context, dashboardID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.QueryDashboardPage(ctx, dashboardID, "", filters)
}

func (m Metrics) QueryCompiledFilterOptions(ctx context.Context, dashboardID string, query dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
	provider, ok := m.Metrics.(interface {
		QueryCompiledFilterOptions(context.Context, string, dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error)
	})
	if !ok {
		return dashboardfilter.OptionResult{}, errors.New("compiled filter options are not supported by this runtime")
	}
	return provider.QueryCompiledFilterOptions(dataquery.WithGovernor(ctx, m), dashboardID, query)
}

func (m Metrics) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.Metrics.QueryDashboardPage(dataquery.WithGovernor(ctx, m), dashboardID, pageID, filters)
}

func (m Metrics) QueryDashboardVisualizations(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.Metrics.QueryDashboardVisualizations(dataquery.WithGovernor(ctx, m), dashboardID, pageID, filters)
}

func (m Metrics) QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	return m.Metrics.QueryVisualization(dataquery.WithGovernor(ctx, m), dashboardID, pageID, filters, visualID)
}

// QueryVisualizationForDefinition preserves the canonical compiled-definition
// execution seam through authorization. The governor context is retained so
// the underlying execution observes the same authorization boundary.
func (m Metrics) QueryVisualizationForDefinition(ctx context.Context, definition dashboarddefinition.Definition, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	provider, ok := m.Metrics.(interface {
		QueryVisualizationForDefinition(context.Context, dashboarddefinition.Definition, string, dashboard.Filters, string) (visualizationir.VisualizationEnvelope, error)
	})
	if !ok {
		return visualizationir.VisualizationEnvelope{}, errors.New("compiled visualization execution is not supported by this runtime")
	}
	return provider.QueryVisualizationForDefinition(dataquery.WithGovernor(ctx, m), definition, pageID, filters, visualID)
}

// DefaultFiltersForDefinition forwards authored defaults through the
// authorization decorator; filter defaults do not execute a data query.
func (m Metrics) DefaultFiltersForDefinition(definition dashboarddefinition.Definition) dashboard.Filters {
	provider, ok := m.Metrics.(interface {
		DefaultFiltersForDefinition(dashboarddefinition.Definition) dashboard.Filters
	})
	if !ok {
		return dashboard.Filters{}.WithDefaults()
	}
	return provider.DefaultFiltersForDefinition(definition)
}

func (m Metrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return m.Metrics.QueryVisualizationWindow(dataquery.WithGovernor(ctx, m), dashboardID, pageID, filters, request)
}

func (m Metrics) QueryVisualizationTile(ctx context.Context, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	port, ok := m.Metrics.(interface {
		QueryVisualizationTile(context.Context, string, string, string, int, int, int) (dashboardruntime.SpatialTileResult, error)
	})
	if !ok {
		return dashboardruntime.SpatialTileResult{}, errors.New("spatial tile metrics are not configured")
	}
	return port.QueryVisualizationTile(dataquery.WithGovernor(ctx, m), dashboardID, visualID, revision, zoom, x, y)
}

func (m Metrics) QueryPublicVisualizationTile(ctx context.Context, publicID, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	port, ok := m.Metrics.(interface {
		QueryPublicVisualizationTile(context.Context, string, string, string, string, int, int, int) (dashboardruntime.SpatialTileResult, error)
	})
	if !ok {
		return dashboardruntime.SpatialTileResult{}, errors.New("public spatial tile metrics are not configured")
	}
	return port.QueryPublicVisualizationTile(dataquery.WithGovernor(ctx, m), publicID, dashboardID, visualID, revision, zoom, x, y)
}

func (m Metrics) applyDataPolicies(ctx context.Context, request dataquery.Query, objects []access.ResourceRef, resourceIndex projectResourceIndex) (dataquery.Query, []accesssnapshot.DataPolicy, error) {
	policies, err := m.effectiveDataPolicies(ctx, request, objects, resourceIndex)
	if err != nil {
		return request, nil, err
	}
	composition, err := composeDataPolicies(policies.active, policies.mandatory)
	if err != nil {
		return request, nil, err
	}
	policyFilters, err := m.resolvePolicyFilterDatasets(request, composition.Filters)
	if err != nil {
		return request, nil, err
	}
	request.Filters = append(request.Filters, policyFilters...)
	columnMasks, err := selectedColumnMasks(request, composition.Masks)
	if err != nil {
		return request, nil, err
	}
	request.ColumnMasks = append(request.ColumnMasks, columnMasks...)
	return request, policies.all(), nil
}

func (m Metrics) resolvePolicyFilterDatasets(request dataquery.Query, filters []dataquery.Filter) ([]dataquery.Filter, error) {
	if request.Kind != dataquery.KindSemanticAggregate || request.Target != "" {
		return filters, nil
	}
	model, ok := m.Metrics.SemanticModel(request.ModelID)
	if !ok || model == nil {
		return filters, nil
	}
	planner, ok := m.concretePlanner(request.ModelID)
	if !ok {
		return nil, fmt.Errorf("compiled semantic planner for model %q is unavailable", request.ModelID)
	}
	dependencies, err := planner.ResolveDependencies(semanticquery.Request{
		Dimensions: dataFieldsToSemanticFields(request.Fields), Metrics: dataFieldsToSemanticFields(request.Metrics),
		Time: semanticquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
	})
	if err != nil {
		return nil, fmt.Errorf("resolve policy filter datasets: %w", err)
	}
	out := make([]dataquery.Filter, 0, len(filters))
	for _, filter := range filters {
		resolved, err := resolvePolicyFilterDataset(model, dependencies.Datasets, filter)
		if err != nil {
			return nil, err
		}
		out = append(out, resolved...)
	}
	return out, nil
}

func resolvePolicyFilterDataset(model *semanticmodel.Model, datasets []string, filter dataquery.Filter) ([]dataquery.Filter, error) {
	if len(filter.Groups) > 0 {
		resolved := filter
		resolved.Groups = make([]dataquery.FilterGroup, len(filter.Groups))
		for groupIndex, group := range filter.Groups {
			children := make([]dataquery.Filter, 0, len(group.Filters))
			for _, child := range group.Filters {
				resolvedChildren, err := resolvePolicyFilterDataset(model, datasets, child)
				if err != nil {
					return nil, err
				}
				children = append(children, resolvedChildren...)
			}
			resolved.Groups[groupIndex] = dataquery.FilterGroup{Filters: children}
		}
		return []dataquery.Filter{resolved}, nil
	}
	if filter.Dataset != "" || (filter.Field == "" && filter.Spatial == nil) {
		return []dataquery.Filter{filter}, nil
	}

	refs := []string{filter.Field}
	if filter.Spatial != nil {
		if filter.Spatial.Dataset != "" {
			return []dataquery.Filter{filter}, nil
		}
		refs = []string{filter.Spatial.LatitudeField, filter.Spatial.LongitudeField}
	}
	tables := map[string]struct{}{}
	for _, ref := range refs {
		if _, conformed := model.Dimensions[ref]; conformed {
			continue
		}
		physical, err := model.ResolveDimension(ref)
		if err != nil {
			return nil, fmt.Errorf("resolve policy filter field %q: %w", ref, err)
		}
		tables[physical.Table] = struct{}{}
	}
	if len(tables) == 0 {
		return []dataquery.Filter{filter}, nil
	}

	resolved := make([]dataquery.Filter, 0, len(datasets))
	for _, dataset := range datasets {
		compatible := true
		for table := range tables {
			if _, err := model.SafeRelationshipPath(dataset, table); err != nil {
				compatible = false
				break
			}
		}
		if !compatible {
			continue
		}
		copy := filter
		if copy.Spatial != nil {
			spatial := *copy.Spatial
			spatial.Dataset = dataset
			copy.Spatial = &spatial
		} else {
			copy.Dataset = dataset
		}
		resolved = append(resolved, copy)
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("policy filter fields %s are not reachable from participating datasets %s", strings.Join(refs, ", "), strings.Join(datasets, ", "))
	}
	return resolved, nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

type effectiveDataPolicySet struct {
	active    []accesssnapshot.DataPolicy
	mandatory []accesssnapshot.DataPolicy
}

func (set effectiveDataPolicySet) all() []accesssnapshot.DataPolicy {
	return append(append([]accesssnapshot.DataPolicy(nil), set.active...), set.mandatory...)
}

func (m Metrics) effectiveDataPolicies(ctx context.Context, request dataquery.Query, objects []access.ResourceRef, resourceIndex projectResourceIndex) (effectiveDataPolicySet, error) {
	snapshot, err := m.authorizationSnapshot(ctx, request.ProjectID)
	if err != nil {
		return effectiveDataPolicySet{}, err
	}
	subjects, err := m.subjects(ctx, request.PrincipalID)
	if err != nil {
		return effectiveDataPolicySet{}, err
	}
	seenObjects := map[string]struct{}{}
	seenPolicies := map[string]struct{}{}
	out := effectiveDataPolicySet{}
	addObject := func(object access.ResourceRef) error {
		key := object.CanonicalID()
		if _, ok := seenObjects[key]; ok {
			return nil
		}
		seenObjects[key] = struct{}{}
		for _, policy := range snapshot.DataPolicies() {
			if policy.Resource.ID() != object.ID() || policy.Resource.Kind() != object.Kind() {
				continue
			}
			if policy.Subject != nil {
				applicable := false
				for _, subject := range subjects {
					if *policy.Subject == subject {
						applicable = true
						break
					}
				}
				if !applicable {
					continue
				}
			}
			if _, ok := seenPolicies[policy.ID]; ok {
				continue
			}
			seenPolicies[policy.ID] = struct{}{}
			out.active = append(out.active, policy)
		}
		return nil
	}
	for _, object := range objects {
		if err := addObject(object); err != nil {
			return effectiveDataPolicySet{}, err
		}
	}
	for _, object := range m.dataQueryColumnObjects(resourceIndex, request) {
		if err := addObject(object); err != nil {
			return effectiveDataPolicySet{}, err
		}
	}
	if projectResource, err := access.NewResourceRef(request.ProjectID, projectgraph.KindProject); err == nil {
		if err := addObject(projectResource); err != nil {
			return effectiveDataPolicySet{}, err
		}
	}
	if candidate, ok := candidateQueryCapabilityFromContext(ctx); ok {
		// Candidate restrictions are appended without deduplicating against the
		// active policy IDs. An authored policy can therefore never shadow or
		// replace a currently effective restriction.
		relevant := map[string]struct{}{}
		for _, object := range append(append([]access.ResourceRef(nil), objects...), m.dataQueryColumnObjects(resourceIndex, request)...) {
			relevant[object.CanonicalID()] = struct{}{}
		}
		for _, restriction := range candidate.Restrictions {
			if err := restriction.Resource.Validate(); err != nil {
				return effectiveDataPolicySet{}, fmt.Errorf("candidate restriction %q resource is invalid: %w", restriction.ID, err)
			}
			if _, ok := relevant[restriction.Resource.CanonicalID()]; ok {
				out.mandatory = append(out.mandatory, restriction)
			}
		}
	}
	return out, nil
}

type effectivePolicyIdentity struct {
	PrincipalID      string                      `json:"principalId"`
	ActorPrincipalID string                      `json:"actorPrincipalId,omitempty"`
	ProjectID        projectgraph.ResourceID     `json:"projectId"`
	Capability       access.Capability           `json:"capability"`
	CandidateID      string                      `json:"candidateId,omitempty"`
	CandidateDigest  string                      `json:"candidateDigest,omitempty"`
	CredentialID     string                      `json:"credentialId,omitempty"`
	Mode             string                      `json:"mode,omitempty"`
	Objects          []string                    `json:"objects"`
	Policies         []accesssnapshot.DataPolicy `json:"policies"`
}

type effectivePolicyContext struct {
	ActorPrincipalID string
	CandidateDigest  string
	CredentialID     string
	Mode             string
}

func effectivePolicyFingerprint(
	request dataquery.Query,
	capability access.Capability,
	objects []access.ResourceRef,
	policies []accesssnapshot.DataPolicy,
	policyContext effectivePolicyContext,
) string {
	objectIDs := make([]string, 0, len(objects))
	for _, object := range objects {
		objectIDs = append(objectIDs, object.CanonicalID())
	}
	sort.Strings(objectIDs)
	policyCopy := append([]accesssnapshot.DataPolicy(nil), policies...)
	sort.Slice(policyCopy, func(i, j int) bool {
		left, _ := json.Marshal(policyCopy[i])
		right, _ := json.Marshal(policyCopy[j])
		return string(left) < string(right)
	})
	identity := effectivePolicyIdentity{
		PrincipalID:      request.PrincipalID,
		ActorPrincipalID: policyContext.ActorPrincipalID,
		ProjectID:        request.ProjectID,
		Capability:       capability,
		CandidateID:      request.CandidateID,
		CandidateDigest:  policyContext.CandidateDigest,
		CredentialID:     policyContext.CredentialID,
		Mode:             policyContext.Mode,
		Objects:          objectIDs,
		Policies:         policyCopy,
	}
	bytes, _ := json.Marshal(identity)
	sum := sha256.Sum256(bytes)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func currentCredentialID(ctx context.Context, metrics Metrics) string {
	if credential, ok := metrics.currentCredential(ctx); ok {
		return credential.Token.ID
	}
	return ""
}

func dataQueryCapability(request dataquery.Query) access.Capability {
	switch request.Operation {
	case dataquery.OperationAPIPreview, dataquery.OperationPreviewWindow:
		return access.CapabilityResourceRead
	}
	switch request.Kind {
	case dataquery.KindModelRows:
		return access.CapabilityResourceRead
	case dataquery.KindSemanticRows:
		return access.CapabilityResourceUse
	default:
		return access.CapabilityResourceUse
	}
}

func (m Metrics) dataQueryObjects(resourceIndex projectResourceIndex, request dataquery.Query) []access.ResourceRef {
	modelID := request.ModelID
	objects := []access.ResourceRef{}
	switch request.Kind {
	case dataquery.KindModelRows:
		// Model-row previews select a semantic dataset alias, but graph
		// authorization is owned by the backing logical Model. Resolve the
		// alias through the activation-owned planner so the executor can retain
		// the alias as its target while access and policy lookup use the
		// canonical Model name.
		if planner, ok := m.concretePlanner(request.ModelID); ok {
			if dataset, ok := planner.Dataset(request.Target); ok {
				if object, ok := resourceIndex.byName(dataset.ModelName(), projectgraph.KindModel); ok {
					objects = append(objects, object)
				}
			}
		}
	default:
		if request.Target != "" {
			if object, ok := resourceIndex.byName(request.Target, projectgraph.KindModel); ok {
				objects = append(objects, object)
			}
		}
	}
	if modelID != "" {
		if object, ok := resourceIndex.byID(modelID, projectgraph.KindSemanticModel); ok {
			objects = append(objects, object)
		}
	}
	return objects
}

func (m Metrics) dataQueryColumnObjects(resourceIndex projectResourceIndex, request dataquery.Query) []access.ResourceRef {
	objects := []access.ResourceRef{}
	for _, field := range dataQuerySelectedFields(request) {
		table, column, ok := splitFieldRef(field)
		if !ok {
			continue
		}
		_ = column
		if parent, ok := resourceIndex.byName(table, projectgraph.KindModel); ok {
			objects = append(objects, parent)
		}
	}
	return objects
}

func dataQuerySelectedFields(request dataquery.Query) []string {
	fields := make([]string, 0, len(request.Fields)+len(request.Metrics)+len(request.AuthorizationFields)+1)
	for _, field := range request.Fields {
		if field.Field != "" {
			fields = append(fields, field.Field)
		}
	}
	for _, field := range request.Metrics {
		if field.Field != "" {
			fields = append(fields, field.Field)
		}
	}
	for _, field := range request.AuthorizationFields {
		if field.Field != "" {
			fields = append(fields, field.Field)
		}
	}
	if request.Value.Field != "" {
		fields = append(fields, request.Value.Field)
	}
	return fields
}

func selectedMaskedFields(request dataquery.Query, mask columnMaskPolicy) []string {
	selected := map[string]string{}
	leafSelected := map[string]string{}
	ambiguousLeaf := map[string]bool{}
	for _, field := range dataQuerySelectedFields(request) {
		normalized := strings.ToLower(strings.TrimSpace(field))
		selected[normalized] = field
		leaf := strings.ToLower(strings.TrimSpace(fieldNameLeaf(field)))
		if existing, ok := leafSelected[leaf]; ok && existing != field {
			ambiguousLeaf[leaf] = true
		} else {
			leafSelected[leaf] = field
		}
	}
	out := []string{}
	seen := map[string]struct{}{}
	for _, field := range mask.Fields {
		key := strings.ToLower(strings.TrimSpace(field))
		selectedField, ok := selected[key]
		if !ok {
			leaf := strings.ToLower(strings.TrimSpace(fieldNameLeaf(field)))
			if ambiguousLeaf[leaf] {
				continue
			}
			selectedField, ok = leafSelected[leaf]
			if !ok {
				continue
			}
		}
		seenKey := strings.ToLower(strings.TrimSpace(selectedField))
		if _, ok := seen[seenKey]; ok {
			continue
		}
		seen[seenKey] = struct{}{}
		out = append(out, selectedField)
	}
	return out
}

func fieldNameLeaf(field string) string {
	_, column, ok := splitFieldRef(field)
	if !ok {
		return field
	}
	return column
}

func splitFieldRef(field string) (string, string, bool) {
	table, column, ok := strings.Cut(strings.TrimSpace(field), ".")
	return table, column, ok && table != "" && column != ""
}

func rejectedDataQueryResult(err error) (dataquery.Result, error) {
	return dataquery.Result{Status: dataquery.StatusError, ExecutionState: dataquery.ExecutionRejected, Error: err.Error()}, err
}

func (m Metrics) currentPrincipal(ctx context.Context) (Principal, bool) {
	if m.principalFromContext == nil {
		return Principal{}, false
	}
	return m.principalFromContext(ctx)
}

func (m Metrics) currentCredential(ctx context.Context) (access.APICredential, bool) {
	if m.credentialFromContext == nil {
		return access.APICredential{}, false
	}
	return m.credentialFromContext(ctx)
}

func (m Metrics) authorizationSnapshot(ctx context.Context, projectID projectgraph.ResourceID) (accesssnapshot.AuthorizationSnapshot, error) {
	if m.snapshotFromContext == nil {
		return accesssnapshot.AuthorizationSnapshot{}, errors.New("authorization snapshot is not configured")
	}
	snapshot, err := m.snapshotFromContext(ctx)
	if err != nil {
		return accesssnapshot.AuthorizationSnapshot{}, err
	}
	if err := snapshot.ValidateBound(); err != nil {
		return accesssnapshot.AuthorizationSnapshot{}, err
	}
	if snapshot.Identity().ProjectID != projectID || snapshot.Project().ProjectID() != projectID || m.Metrics.Catalog().Project.ID != projectID {
		return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("authorization snapshot project identity does not match active project %q", projectID)
	}
	return snapshot, nil
}

func (m Metrics) subjects(ctx context.Context, principalID string) ([]access.SubjectRef, error) {
	if m.subjectsFromContext == nil {
		return nil, errors.New("authorization subjects are not configured")
	}
	return m.subjectsFromContext(ctx, principalID)
}

func (m Metrics) capabilityAllowed(ctx context.Context, snapshot accesssnapshot.AuthorizationSnapshot, principalID string, token access.APIToken, capability access.Capability) (bool, error) {
	subjects, err := m.subjects(ctx, principalID)
	if err != nil {
		return false, err
	}
	effective, err := snapshot.EffectiveCapabilities(subjects)
	if err != nil {
		return false, err
	}
	for _, value := range access.IntersectTokenCapabilities(token.Capabilities, effective) {
		if value == capability {
			return true, nil
		}
	}
	return false, nil
}

func (m Metrics) tokenAllowsCapability(ctx context.Context, snapshot accesssnapshot.AuthorizationSnapshot, principalID string, token access.APIToken, capability access.Capability) bool {
	if token.Capabilities == nil {
		return true
	}
	allowed, err := m.capabilityAllowed(ctx, snapshot, principalID, token, capability)
	if err == nil && allowed {
		return true
	}
	return false
}

func (m Metrics) persistCanonicalAudit(ctx context.Context, snapshot accesssnapshot.AuthorizationSnapshot, event access.CanonicalAuditEvent) error {
	if m.auditRecorder == nil {
		return nil
	}
	return access.PersistCanonicalAuditEvent(ctx, m.auditRecorder, event)
}

// projectResourceIndex is the immutable identity bridge used while governing a
// query. Graph IDs identify canonical resources (for example ModelID), while
// executable table/dataset references are symbolic names (for example Target or
// a resolver dependency). Keeping those lookups separate prevents an opaque
// graph ID from leaking into the planner's executable table name.
type projectResourceIndex struct {
	ids   map[projectgraph.ResourceID]projectgraph.Resource
	names map[string]projectgraph.Resource
}

func newProjectResourceIndex(project projectgraph.ProjectGraph) (projectResourceIndex, error) {
	if err := project.Validate(); err != nil {
		return projectResourceIndex{}, fmt.Errorf("validate project resource index graph: %w", err)
	}
	index := projectResourceIndex{
		ids:   make(map[projectgraph.ResourceID]projectgraph.Resource),
		names: make(map[string]projectgraph.Resource),
	}
	for _, resource := range project.Resources() {
		if _, exists := index.ids[resource.ID]; exists {
			return projectResourceIndex{}, fmt.Errorf("duplicate project resource ID %q", resource.ID)
		}
		name := strings.TrimSpace(resource.Name)
		if name == "" || name != resource.Name {
			return projectResourceIndex{}, fmt.Errorf("invalid project resource name %q", resource.Name)
		}
		if _, exists := index.names[name]; exists {
			return projectResourceIndex{}, fmt.Errorf("duplicate project resource name %q", name)
		}
		index.ids[resource.ID] = resource
		index.names[name] = resource
	}
	return index, nil
}

func (index projectResourceIndex) byID(id string, kind projectgraph.Kind) (access.ResourceRef, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return access.ResourceRef{}, false
	}
	resource, ok := index.ids[projectgraph.ResourceID(id)]
	if !ok || resource.Kind != kind {
		return access.ResourceRef{}, false
	}
	ref, err := access.NewResourceRef(resource.ID, kind)
	return ref, err == nil
}

func (index projectResourceIndex) byName(name string, kind projectgraph.Kind) (access.ResourceRef, bool) {
	// Symbolic references use the graph's declared name grammar. Do not
	// normalize whitespace here: a padded value is not the same executable
	// table name and must fail closed rather than becoming an implicit alias.
	if name == "" || strings.TrimSpace(name) != name {
		return access.ResourceRef{}, false
	}
	resource, ok := index.names[name]
	if !ok || resource.Kind != kind {
		return access.ResourceRef{}, false
	}
	ref, err := access.NewResourceRef(resource.ID, kind)
	return ref, err == nil
}

func canonicalResourceByID(project projectgraph.ProjectGraph, id string, kind projectgraph.Kind) (access.ResourceRef, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return access.ResourceRef{}, false
	}
	resourceID := projectgraph.ResourceID(id)
	if resource, ok := project.Resource(resourceID); ok && resource.Kind == kind {
		ref, err := access.NewResourceRef(resource.ID, kind)
		return ref, err == nil
	}
	return access.ResourceRef{}, false
}

func dashboardPublicationSubjectID(projectID projectgraph.ResourceID, publication string) string {
	return "dashboard_publication:" + projectID.String() + "." + strings.TrimSpace(publication)
}
