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
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type Principal struct {
	ID        string
	DevBypass bool
}

type Options struct {
	Repo                  access.DataAuthorizationService
	PrincipalFromContext  func(context.Context) (Principal, bool)
	CredentialFromContext func(context.Context) (access.APICredential, bool)
	TokenAllows           func(access.APIToken, string, access.Privilege) bool
}

type Metrics struct {
	queryruntime.Metrics
	repo                  access.DataAuthorizationService
	principalFromContext  func(context.Context) (Principal, bool)
	credentialFromContext func(context.Context) (access.APICredential, bool)
	tokenAllows           func(access.APIToken, string, access.Privilege) bool
}

type DeniedError struct {
	PrincipalID string
	Privilege   access.Privilege
	Credential  bool
}

func (e DeniedError) Error() string {
	if e.Credential {
		return fmt.Sprintf("data query credential lacks %s", e.Privilege)
	}
	return fmt.Sprintf("principal %q lacks %s on data object", e.PrincipalID, e.Privilege)
}

func IsDenied(err error) bool {
	var denied DeniedError
	return errors.As(err, &denied)
}

func New(metrics queryruntime.Metrics, options Options) Metrics {
	return Metrics{
		Metrics:               metrics,
		repo:                  options.Repo,
		principalFromContext:  options.PrincipalFromContext,
		credentialFromContext: options.CredentialFromContext,
		tokenAllows:           options.TokenAllows,
	}
}

func (m Metrics) MetricsForWorkspace(workspaceID string) (queryruntime.Metrics, bool) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, false
	}
	provider, ok := m.Metrics.(queryruntime.WorkspaceMetrics)
	if ok {
		metrics, ok := provider.MetricsForWorkspace(workspaceID)
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
	if catalog.Workspace.ID == workspaceID {
		return m, true
	}
	return nil, false
}

func (m Metrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	if m.Metrics == nil {
		return dataquery.Result{}, errors.New("query metrics are not configured")
	}
	if m.repo == nil {
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
	if m.repo == nil {
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
		privilege := dataQueryPrivilege(governed)
		if candidateQuery {
			privilege = access.PrivilegePreviewData
		}
		if err := m.recordDataAccessAudit(ctx, governed, privilege, dataQueryObjects(governed), "started", nil); err != nil {
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
	if strings.TrimSpace(request.WorkspaceID) == "" {
		return request, nil, errors.New("workspace ID is required")
	}
	candidateCapability, candidateQuery := candidateQueryCapabilityFromContext(ctx)
	privilege := dataQueryPrivilege(request)
	if candidateQuery {
		// Candidate execution is always preview, even when it renders a normal
		// dashboard interaction whose production equivalent uses QUERY_DATA.
		privilege = access.PrivilegePreviewData
	}
	objects := dataQueryObjects(request)
	capability, publicationQuery := dashboardPublicationCapabilityFromContext(ctx)
	viewAs, viewAsQuery := viewAsCapabilityFromContext(ctx)
	semanticObjects, physicalObjects, err := m.resolvedDependencyObjects(request, publicationQuery)
	if err != nil {
		return request, nil, err
	}
	objects = append(objects, semanticObjects...)
	objects = append(objects, physicalObjects...)
	if publicationQuery {
		if candidateQuery || viewAsQuery || request.CandidateID != "" {
			request.PrincipalID = access.DashboardPublicationSubjectID(capability.WorkspaceID, capability.Publication)
			err := errors.New("public query cannot use candidate or view-as authority")
			if auditErr := m.recordDataAccessAudit(ctx, request, access.PrivilegeQueryData, objects, "denied", err); auditErr != nil {
				return request, nil, errors.Join(err, auditErr)
			}
			return request, nil, err
		}
		objects = append(objects, dataQueryColumnObjects(request)...)
		request.PrincipalID = access.DashboardPublicationSubjectID(capability.WorkspaceID, capability.Publication)
		if err := validateDashboardPublicationQuery(capability, request, objects); err != nil {
			if auditErr := m.recordDataAccessAudit(ctx, request, access.PrivilegeQueryData, objects, "denied", err); auditErr != nil {
				return request, nil, errors.Join(err, auditErr)
			}
			return request, nil, err
		}
		governed, policies, err := m.applyDataPolicies(ctx, request, objects)
		if err != nil {
			if auditErr := m.recordDataAccessAudit(ctx, request, access.PrivilegeQueryData, objects, "error", err); auditErr != nil {
				return request, nil, errors.Join(err, auditErr)
			}
			return request, nil, err
		}
		governed.EffectivePolicyFingerprint = effectivePolicyFingerprint(
			governed,
			access.PrivilegeQueryData,
			objects,
			policies,
			effectivePolicyContext{},
		)
		return governed, func(result *dataquery.Result, executeErr error) error {
			status := "success"
			if executeErr != nil || (result != nil && result.Status == dataquery.StatusError) {
				status = "error"
			}
			return m.recordDataAccessAudit(ctx, governed, access.PrivilegeQueryData, objects, status, executeErr)
		}, nil
	}
	principalID := strings.TrimSpace(request.PrincipalID)
	principal, authenticated := m.currentPrincipal(ctx)
	if authenticated {
		if principalID != "" && principalID != principal.ID {
			request.PrincipalID = principal.ID
			err := DeniedError{PrincipalID: principal.ID, Privilege: privilege}
			_ = m.recordDataAccessAudit(ctx, request, privilege, objects, "denied", err)
			return request, nil, err
		}
		if !candidateQuery && request.CandidateID != "" {
			request.PrincipalID = principal.ID
			err := DeniedError{PrincipalID: principal.ID, Privilege: privilege}
			_ = m.recordDataAccessAudit(ctx, request, privilege, objects, "denied", err)
			return request, nil, err
		}
		if candidateQuery {
			var err error
			request, err = validateCandidateQueryCapability(candidateCapability, principal, request)
			if err != nil {
				denied := DeniedError{PrincipalID: principal.ID, Privilege: privilege}
				_ = m.recordDataAccessAudit(ctx, request, privilege, objects, "denied", err)
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
				privilege,
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
		_ = m.recordDataAccessAudit(ctx, request, privilege, objects, "denied", err)
		return request, nil, err
	}
	if viewAsQuery && !authenticated {
		err := dataquery.ErrMissingPrincipal
		_ = m.recordDataAccessAudit(ctx, request, privilege, objects, "denied", err)
		return request, nil, err
	}
	if principalID == "" {
		err := dataquery.ErrMissingPrincipal
		_ = m.recordDataAccessAudit(ctx, request, "", objects, "denied", err)
		return request, nil, err
	}
	if credential, ok := m.currentCredential(ctx); ok && !m.allowsToken(credential.Token, request.WorkspaceID, privilege) {
		err := DeniedError{PrincipalID: principalID, Privilege: privilege, Credential: true}
		_ = m.recordDataAccessAudit(ctx, request, privilege, objects, "denied", err)
		return request, nil, err
	}
	if ok, err := m.authorizeDataQuery(ctx, principalID, privilege, request, objects); err != nil {
		_ = m.recordDataAccessAudit(ctx, request, privilege, objects, "error", err)
		return request, nil, err
	} else if !ok {
		err := DeniedError{PrincipalID: principalID, Privilege: privilege}
		_ = m.recordDataAccessAudit(ctx, request, privilege, objects, "denied", err)
		return request, nil, err
	}
	governed, policies, err := m.applyDataPolicies(ctx, request, objects)
	if err != nil {
		_ = m.recordDataAccessAudit(ctx, request, privilege, objects, "error", err)
		return request, nil, err
	}
	candidateDigest := ""
	if candidateQuery {
		candidateDigest = candidateCapability.PolicyDigest
	}
	governed.EffectivePolicyFingerprint = effectivePolicyFingerprint(
		governed,
		privilege,
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
			auditErr := m.recordDataAccessAudit(ctx, governed, privilege, objects, "error", executeErr)
			if strictAudit && auditErr != nil {
				return errors.Join(executeErr, auditErr)
			}
			return nil
		}
		status := "success"
		if result != nil && result.Status == dataquery.StatusError {
			status = "error"
		}
		auditErr := m.recordDataAccessAudit(ctx, governed, privilege, objects, status, nil)
		if strictAudit {
			return auditErr
		}
		return nil
	}, nil
}

func (m Metrics) authorizeDataQuery(ctx context.Context, principalID string, privilege access.Privilege, request dataquery.Query, objects []access.ObjectRef) (bool, error) {
	if request.Kind == dataquery.KindSemanticAggregate && request.Target == "" {
		modelObject := access.ItemObject(access.SecurableSemanticModel, request.WorkspaceID, request.ModelID)
		decision, err := m.repo.Authorize(ctx, principalID, privilege, modelObject)
		if err != nil || !decision.Allowed {
			return decision.Allowed, err
		}
		for _, object := range objects {
			if object.Type != access.SecurableSemanticField {
				continue
			}
			decision, err := m.repo.Authorize(ctx, principalID, privilege, object)
			if err != nil || !decision.Allowed {
				return decision.Allowed, err
			}
		}
		return true, nil
	}
	decision, err := m.repo.AuthorizeAny(ctx, principalID, privilege, objects)
	if err != nil || decision.Allowed {
		return decision.Allowed, err
	}
	columnObjects := dataQueryColumnObjects(request)
	if len(columnObjects) == 0 {
		return false, nil
	}
	for _, column := range columnObjects {
		columnDecision, err := m.repo.Authorize(ctx, principalID, privilege, column)
		if err != nil {
			return false, err
		}
		if !columnDecision.Allowed {
			return false, nil
		}
	}
	return true, nil
}

func (m Metrics) resolvedDependencyObjects(request dataquery.Query, includePublicInteractions bool) ([]access.ObjectRef, []access.ObjectRef, error) {
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
	measures := dataFieldsToSemanticFields(request.Measures)
	for _, field := range request.AuthorizationFields {
		if semanticFieldIsMeasure(model, field.Field) {
			measures = append(measures, semanticquery.Field{Field: field.Field, Alias: field.Alias})
		} else {
			dimensions = append(dimensions, semanticquery.Field{Field: field.Field, Alias: field.Alias})
		}
	}
	if request.Value.Field != "" {
		field := semanticquery.Field{Field: request.Value.Field, Alias: request.Value.Alias}
		if semanticFieldIsMeasure(model, request.Value.Field) {
			measures = append(measures, field)
		} else {
			dimensions = append(dimensions, field)
		}
	}
	queryRequest := semanticquery.Request{
		Table:      request.Target,
		Dimensions: dimensions,
		Measures:   measures,
		Time:       semanticquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
		Filters:    dataFiltersToSemanticFilters(request.Filters),
		Sort:       dataSortToSemanticSort(request.Sort),
	}
	dependencies, err := semanticquery.ResolveDependencies(model, queryRequest)
	if err != nil {
		return nil, nil, err
	}
	modelObject := access.ItemObject(access.SecurableSemanticModel, request.WorkspaceID, request.ModelID)
	semanticObjects := make([]access.ObjectRef, 0, len(dependencies.LogicalFields))
	for _, field := range dependencies.LogicalFields {
		if !isSemanticField(model, field) {
			continue
		}
		semanticObjects = append(semanticObjects, access.ItemObjectWithParent(access.SecurableSemanticField, request.WorkspaceID, request.ModelID+"/"+field, modelObject))
	}
	physicalObjects := make([]access.ObjectRef, 0, len(dependencies.Facts)+len(dependencies.PhysicalFields))
	datasets := map[string]access.ObjectRef{}
	for _, fact := range dependencies.Facts {
		dataset := access.ItemObjectWithParent(access.SecurableDataset, request.WorkspaceID, request.ModelID+"/"+fact, modelObject)
		datasets[fact] = dataset
		physicalObjects = append(physicalObjects, dataset)
	}
	for _, field := range dependencies.PhysicalFields {
		table, column, ok := splitFieldRef(field)
		if !ok {
			continue
		}
		tableObject, ok := datasets[table]
		if !ok {
			tableObject = access.ItemObjectWithParent(access.SecurableDataset, request.WorkspaceID, request.ModelID+"/"+table, modelObject)
			datasets[table] = tableObject
			physicalObjects = append(physicalObjects, tableObject)
		}
		physicalObjects = append(physicalObjects, access.ItemObjectWithParent(access.SecurableColumn, request.WorkspaceID, request.ModelID+"/"+table+"/"+column, tableObject))
	}
	return semanticObjects, physicalObjects, nil
}

func semanticFieldIsMeasure(model *semanticmodel.Model, field string) bool {
	if model == nil {
		return false
	}
	if _, ok := model.Measures[field]; ok {
		return true
	}
	_, ok := model.Metrics[field]
	return ok
}

func isSemanticField(model *semanticmodel.Model, field string) bool {
	if _, ok := model.Dimensions[field]; ok {
		return true
	}
	if _, ok := model.Measures[field]; ok {
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
		out = append(out, semanticquery.Filter{Field: filter.Field, Fact: filter.Fact, Operator: filter.Operator, Values: append([]any{}, filter.Values...), Groups: groups, Spatial: dataSpatialFilterToSemantic(filter.Spatial)})
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
		Kind: value.Kind, LatitudeField: value.LatitudeField, LongitudeField: value.LongitudeField, Fact: value.Fact,
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

func (m Metrics) recordDataAccessAudit(ctx context.Context, request dataquery.Query, privilege access.Privilege, objects []access.ObjectRef, status string, cause error) error {
	if m.repo == nil {
		return nil
	}
	action := "data_query.executed"
	if privilege == access.PrivilegePreviewData {
		action = "data_preview.executed"
	}
	targetType := strings.TrimSpace(request.ObjectType)
	targetID := strings.TrimSpace(request.ObjectID)
	if targetType == "" || targetID == "" {
		for _, object := range objects {
			if object.Type == "" {
				continue
			}
			if targetType == "" {
				targetType = string(object.Type)
			}
			if targetID == "" {
				targetID = object.CanonicalID()
			}
			break
		}
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
	return access.PersistAuditEvent(ctx, m.repo, access.AuditEventInput{
		WorkspaceID:   request.WorkspaceID,
		PrincipalID:   request.PrincipalID,
		Action:        action,
		TargetType:    targetType,
		TargetID:      targetID,
		Privilege:     privilege,
		Status:        status,
		RequestID:     request.RequestID,
		CorrelationID: request.CorrelationID,
		MetadataJSON:  string(bytes),
	})
}

func (m Metrics) QuerySemantic(ctx context.Context, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	result, err := m.ExecuteDataQuery(ctx, semanticAggregateDataQuery(modelID, request))
	return queryRowsFromDataResult(result.Rows), err
}

func (m Metrics) PreviewSemantic(ctx context.Context, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	result, err := m.ExecuteDataQuery(ctx, semanticRowsDataQuery(modelID, request))
	return queryRowsFromDataResult(result.Rows), err
}

func semanticAggregateDataQuery(modelID string, request reportdef.AggregateQuery) dataquery.Query {
	return dataquery.Query{
		ModelID:  modelID,
		Kind:     dataquery.KindSemanticAggregate,
		Target:   request.Table,
		Fields:   queryFieldsToDataFields(request.Dimensions),
		Measures: queryFieldsToDataFields(request.Measures),
		Time:     dataquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
		Filters:  queryFiltersToDataFilters(request.Filters),
		Sort:     querySortToDataSort(request.Sort),
		Limit:    request.Limit,
		Offset:   request.Offset,
	}
}

func semanticRowsDataQuery(modelID string, request reportdef.RowQuery) dataquery.Query {
	return dataquery.Query{
		ModelID:  modelID,
		Kind:     dataquery.KindSemanticRows,
		Target:   request.Table,
		Fields:   queryFieldsToDataFields(request.Dimensions),
		Measures: queryFieldsToDataFields(request.Measures),
		Filters:  queryFiltersToDataFilters(request.Filters),
		Sort:     querySortToDataSort(request.Sort),
		Limit:    request.Limit,
		Offset:   request.Offset,
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
			Fact:     filter.Fact,
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

func (m Metrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return m.Metrics.QueryVisualizationWindow(dataquery.WithGovernor(ctx, m), dashboardID, pageID, filters, request)
}

func (m Metrics) QueryVisualizationTile(ctx context.Context, workspaceID, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	scopedMetrics, found := m.MetricsForWorkspace(workspaceID)
	scoped, ok := scopedMetrics.(Metrics)
	if !found || !ok {
		return dashboardruntime.SpatialTileResult{}, errors.New("workspace spatial tile metrics are not configured")
	}
	port, ok := scoped.Metrics.(interface {
		QueryVisualizationTile(context.Context, string, string, string, string, int, int, int) (dashboardruntime.SpatialTileResult, error)
	})
	if !ok {
		return dashboardruntime.SpatialTileResult{}, errors.New("spatial tile metrics are not configured")
	}
	return port.QueryVisualizationTile(dataquery.WithGovernor(ctx, scoped), workspaceID, dashboardID, visualID, revision, zoom, x, y)
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

func (m Metrics) applyDataPolicies(ctx context.Context, request dataquery.Query, objects []access.ObjectRef) (dataquery.Query, []access.DataPolicy, error) {
	policies, err := m.effectiveDataPolicies(ctx, request, objects)
	if err != nil {
		return request, nil, err
	}
	composition, err := composeDataPolicies(policies.active, policies.mandatory)
	if err != nil {
		return request, nil, err
	}
	policyFilters, err := m.resolvePolicyFilterFacts(request, composition.Filters)
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

func (m Metrics) resolvePolicyFilterFacts(request dataquery.Query, filters []dataquery.Filter) ([]dataquery.Filter, error) {
	if request.Kind != dataquery.KindSemanticAggregate || request.Target != "" {
		return filters, nil
	}
	model, ok := m.Metrics.SemanticModel(request.ModelID)
	if !ok || model == nil {
		return filters, nil
	}
	dependencies, err := semanticquery.ResolveDependencies(model, semanticquery.Request{
		Dimensions: dataFieldsToSemanticFields(request.Fields), Measures: dataFieldsToSemanticFields(request.Measures),
		Time: semanticquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
	})
	if err != nil {
		return nil, fmt.Errorf("resolve policy filter facts: %w", err)
	}
	out := make([]dataquery.Filter, 0, len(filters))
	for _, filter := range filters {
		resolved, err := resolvePolicyFilterFact(model, dependencies.Facts, filter)
		if err != nil {
			return nil, err
		}
		out = append(out, resolved...)
	}
	return out, nil
}

func resolvePolicyFilterFact(model *semanticmodel.Model, facts []string, filter dataquery.Filter) ([]dataquery.Filter, error) {
	if len(filter.Groups) > 0 {
		resolved := filter
		resolved.Groups = make([]dataquery.FilterGroup, len(filter.Groups))
		for groupIndex, group := range filter.Groups {
			children := make([]dataquery.Filter, 0, len(group.Filters))
			for _, child := range group.Filters {
				resolvedChildren, err := resolvePolicyFilterFact(model, facts, child)
				if err != nil {
					return nil, err
				}
				children = append(children, resolvedChildren...)
			}
			resolved.Groups[groupIndex] = dataquery.FilterGroup{Filters: children}
		}
		return []dataquery.Filter{resolved}, nil
	}
	if filter.Fact != "" || (filter.Field == "" && filter.Spatial == nil) {
		return []dataquery.Filter{filter}, nil
	}

	refs := []string{filter.Field}
	if filter.Spatial != nil {
		if filter.Spatial.Fact != "" {
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

	resolved := make([]dataquery.Filter, 0, len(facts))
	for _, fact := range facts {
		compatible := true
		for table := range tables {
			if _, err := model.SafeRelationshipPath(fact, table); err != nil {
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
			spatial.Fact = fact
			copy.Spatial = &spatial
		} else {
			copy.Fact = fact
		}
		resolved = append(resolved, copy)
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("policy filter fields %s are not reachable from participating facts %s", strings.Join(refs, ", "), strings.Join(facts, ", "))
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
	active    []access.DataPolicy
	mandatory []access.DataPolicy
}

func (set effectiveDataPolicySet) all() []access.DataPolicy {
	return append(append([]access.DataPolicy(nil), set.active...), set.mandatory...)
}

func (m Metrics) effectiveDataPolicies(ctx context.Context, request dataquery.Query, objects []access.ObjectRef) (effectiveDataPolicySet, error) {
	seenObjects := map[string]struct{}{}
	seenPolicies := map[string]struct{}{}
	out := effectiveDataPolicySet{}
	addObject := func(object access.ObjectRef) error {
		if object.Type == "" {
			return nil
		}
		key := object.CanonicalID()
		if _, ok := seenObjects[key]; ok {
			return nil
		}
		seenObjects[key] = struct{}{}
		policies, err := m.repo.ListEffectiveDataPolicies(ctx, request.PrincipalID, object, true)
		if err != nil {
			return err
		}
		for _, policy := range policies {
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
	for _, object := range dataQueryColumnObjects(request) {
		if err := addObject(object); err != nil {
			return effectiveDataPolicySet{}, err
		}
	}
	if candidate, ok := candidateQueryCapabilityFromContext(ctx); ok {
		// Candidate restrictions are appended without deduplicating against the
		// active policy IDs. An authored policy can therefore never shadow or
		// replace a currently effective restriction.
		relevant := map[string]struct{}{
			"workspace:" + request.WorkspaceID: {},
		}
		for _, object := range append(append([]access.ObjectRef(nil), objects...), dataQueryColumnObjects(request)...) {
			relevant[object.CanonicalID()] = struct{}{}
		}
		for _, restriction := range candidate.Restrictions {
			if restriction.ObjectID == "" {
				out.mandatory = append(out.mandatory, restriction)
				continue
			}
			if _, ok := relevant[restriction.ObjectID]; ok {
				out.mandatory = append(out.mandatory, restriction)
			}
		}
	}
	return out, nil
}

type effectivePolicyIdentity struct {
	PrincipalID      string              `json:"principalId"`
	ActorPrincipalID string              `json:"actorPrincipalId,omitempty"`
	WorkspaceID      string              `json:"workspaceId"`
	Privilege        access.Privilege    `json:"privilege"`
	CandidateID      string              `json:"candidateId,omitempty"`
	CandidateDigest  string              `json:"candidateDigest,omitempty"`
	CredentialID     string              `json:"credentialId,omitempty"`
	Mode             string              `json:"mode,omitempty"`
	Objects          []string            `json:"objects"`
	Policies         []access.DataPolicy `json:"policies"`
}

type effectivePolicyContext struct {
	ActorPrincipalID string
	CandidateDigest  string
	CredentialID     string
	Mode             string
}

func effectivePolicyFingerprint(
	request dataquery.Query,
	privilege access.Privilege,
	objects []access.ObjectRef,
	policies []access.DataPolicy,
	policyContext effectivePolicyContext,
) string {
	objectIDs := make([]string, 0, len(objects))
	for _, object := range objects {
		objectIDs = append(objectIDs, object.CanonicalID())
	}
	sort.Strings(objectIDs)
	policyCopy := append([]access.DataPolicy(nil), policies...)
	sort.Slice(policyCopy, func(i, j int) bool {
		left, _ := json.Marshal(policyCopy[i])
		right, _ := json.Marshal(policyCopy[j])
		return string(left) < string(right)
	})
	identity := effectivePolicyIdentity{
		PrincipalID:      request.PrincipalID,
		ActorPrincipalID: policyContext.ActorPrincipalID,
		WorkspaceID:      request.WorkspaceID,
		Privilege:        privilege,
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

func dataQueryPrivilege(request dataquery.Query) access.Privilege {
	switch request.Operation {
	case dataquery.OperationAPIPreview, dataquery.OperationPreviewWindow:
		return access.PrivilegePreviewData
	}
	switch request.Kind {
	case dataquery.KindModelTableRows:
		return access.PrivilegePreviewData
	case dataquery.KindSemanticRows:
		if request.Surface == dataquery.SurfaceDashboard || request.Surface == dataquery.SurfacePublicDashboard {
			return access.PrivilegeQueryData
		}
		return access.PrivilegePreviewData
	default:
		return access.PrivilegeQueryData
	}
}

func dataQueryObjects(request dataquery.Query) []access.ObjectRef {
	workspaceID := request.WorkspaceID
	modelID := request.ModelID
	objects := []access.ObjectRef{}
	switch request.Kind {
	case dataquery.KindModelTableRows:
		objects = append(objects, access.ItemObjectWithParent(access.SecurableModelTable, workspaceID, modelID+"/"+request.Target, access.ItemObject(access.SecurableSemanticModel, workspaceID, modelID)))
	default:
		if request.Target != "" {
			objects = append(objects, access.ItemObjectWithParent(access.SecurableDataset, workspaceID, modelID+"/"+request.Target, access.ItemObject(access.SecurableSemanticModel, workspaceID, modelID)))
		}
	}
	if modelID != "" {
		objects = append(objects, access.ItemObject(access.SecurableSemanticModel, workspaceID, modelID))
	}
	if workspaceID != "" {
		objects = append(objects, access.WorkspaceObject(workspaceID))
	}
	return objects
}

func dataQueryColumnObjects(request dataquery.Query) []access.ObjectRef {
	objects := []access.ObjectRef{}
	for _, field := range dataQuerySelectedFields(request) {
		table, column, ok := splitFieldRef(field)
		if !ok {
			continue
		}
		parent := access.ItemObjectWithParent(access.SecurableDataset, request.WorkspaceID, request.ModelID+"/"+table, access.ItemObject(access.SecurableSemanticModel, request.WorkspaceID, request.ModelID))
		objects = append(objects, access.ItemObjectWithParent(access.SecurableColumn, request.WorkspaceID, request.ModelID+"/"+table+"/"+column, parent))
	}
	return objects
}

func dataQuerySelectedFields(request dataquery.Query) []string {
	fields := make([]string, 0, len(request.Fields)+len(request.Measures)+len(request.AuthorizationFields)+1)
	for _, field := range request.Fields {
		if field.Field != "" {
			fields = append(fields, field.Field)
		}
	}
	for _, field := range request.Measures {
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

func (m Metrics) allowsToken(token access.APIToken, workspaceID string, privilege access.Privilege) bool {
	if m.tokenAllows == nil {
		return true
	}
	return m.tokenAllows(token, workspaceID, privilege)
}
