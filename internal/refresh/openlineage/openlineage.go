// Package openlineage contains the export-only OpenLineage projection for
// refresh pipelines.  The objects in this package are observations: they do
// not grant authority to execute, retry, cancel, or publish a refresh.
package openlineage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
)

const (
	// Producer is the stable producer identifier on exported events and
	// LeapView-owned facets.  It is intentionally independent of a serving
	// generation, binary version, or deployment host.
	Producer = "https://leapview.dev/openlineage"
	// SchemaURL identifies the OpenLineage event contract.  LeapView keeps
	// this URL explicit so an exporter can forward the event without adding
	// transport-specific metadata.
	SchemaURL = "https://openlineage.io/spec/2-0-2/OpenLineage.json"
	// FacetSchemaURL is the schema identifier for the LeapView-scoped facet.
	FacetSchemaURL = "https://leapview.dev/openlineage/facets/refresh.json"
)

// EventType is the OpenLineage lifecycle event type.
type EventType string

const (
	EventStart    EventType = "START"
	EventRunning  EventType = "RUNNING"
	EventComplete EventType = "COMPLETE"
	EventFail     EventType = "FAIL"
	EventAbort    EventType = "ABORT"
	EventOther    EventType = "OTHER"
)

// Facets is intentionally an open map.  OpenLineage facets are extensible,
// and keeping their values as JSON preserves standard and future facets
// without coupling this package to a transport or generated client.
type Facets map[string]json.RawMessage

// Event is the OpenLineage event DTO.  Its JSON shape follows the object model
// (run, job, inputs, outputs, producer, schemaURL, eventType, eventTime).
type Event struct {
	EventType EventType `json:"eventType"`
	EventTime time.Time `json:"eventTime"`
	Run       Run       `json:"run"`
	Job       Job       `json:"job"`
	Inputs    []Dataset `json:"inputs,omitempty"`
	Outputs   []Dataset `json:"outputs,omitempty"`
	Producer  string    `json:"producer"`
	SchemaURL string    `json:"schemaURL"`
}

// Run is an OpenLineage run identifier and its facets.
type Run struct {
	RunID  string `json:"runId"`
	Facets Facets `json:"facets,omitempty"`
}

// Job is an OpenLineage job identifier and its facets.
type Job struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Facets    Facets `json:"facets,omitempty"`
}

// Dataset is an OpenLineage dataset identifier and its facets.  A source or
// materialized Model relation is represented as a dataset; a Model
// definition itself is not emitted as one.
type Dataset struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Facets    Facets `json:"facets,omitempty"`
}

// Pipeline is the immutable context needed to project one authored Pipeline
// and its generation-bound plan.  MaterializationScope and SourceInputs are
// copied before mapping, so callers may safely reuse or mutate their input.
type Pipeline struct {
	// Namespace is optional.  If omitted, NamespaceFor(ProjectID,
	// Environment) is used.
	Namespace            string
	ProjectID            string
	Environment          string
	ID                   string
	SemanticModelID      string
	GenerationID         string
	PlanDigest           string
	SelectionDigest      string
	ExecutionDigest      string
	ProvenanceDigest     string
	GovernanceDigest     string
	EvidenceDigest       string
	QualificationChecks  []string
	MaterializationScope []string
	SourceInputs         []string
}

// FromPipelinePlan adapts the immutable delivery plan to the export context.
// Project and environment are supplied separately because PipelinePlan is
// intentionally generation-bound but does not duplicate serving scope.
func FromPipelinePlan(projectID, environment string, plan projectpipelineplan.Plan) Pipeline {
	return Pipeline{
		ProjectID:            projectID,
		Environment:          environment,
		ID:                   plan.PipelineID,
		SemanticModelID:      plan.SemanticModelID,
		GenerationID:         plan.ServingGenerationID,
		PlanDigest:           plan.Digest,
		SelectionDigest:      plan.SelectionDigest,
		ExecutionDigest:      plan.ExecutionDigest,
		ProvenanceDigest:     plan.ProvenanceDigest,
		GovernanceDigest:     plan.GovernanceDigest,
		EvidenceDigest:       plan.EvidenceDigest,
		QualificationChecks:  append([]string(nil), plan.QualificationChecks...),
		MaterializationScope: append([]string(nil), plan.MaterializationScope...),
		SourceInputs:         append([]string(nil), plan.SourceInputs...),
	}
}

// PipelineRun is the operational observation of one PipelineRun.  NominalTime
// is populated for schedule occurrences and is exported through the standard
// nominalTime run facet.  ModelID and ParentRunID identify a separately
// emitted child Model run; leave both empty for the PipelineRun event.
type PipelineRun struct {
	ID          string
	PipelineID  string
	EventType   EventType
	EventTime   time.Time
	TriggerID   string
	TriggerType string
	NominalTime *time.Time
	ParentRunID string
	ModelID     string
	Inputs      []string
	Outputs     []string
}

// Exporter is the deliberately narrow observability boundary.  Implementors
// may forward an event to a collector, but an exporter has no execution
// authority over the refresh lifecycle.
type Exporter interface {
	Export(context.Context, Event) error
}

// NamespaceFor returns the stable namespace used for jobs and datasets when
// no caller-owned namespace is supplied.  Project and environment are URL
// escaped independently so the namespace remains unambiguous for IDs that
// contain punctuation.
func NamespaceFor(projectID, environment string) string {
	return "leapview://" + url.PathEscape(strings.TrimSpace(projectID)) + "/" + url.PathEscape(strings.TrimSpace(environment))
}

// JobForPipeline maps Pipeline identity and scoped evidence to an
// OpenLineage Job.  The LeapView facet is attached to the Job because plan
// and selection identify what the Pipeline means independent of a run.
func JobForPipeline(p Pipeline) (Job, error) {
	if err := p.validate(); err != nil {
		return Job{}, err
	}
	namespace := p.Namespace
	if namespace == "" {
		namespace = NamespaceFor(p.ProjectID, p.Environment)
	}
	return Job{
		Namespace: namespace,
		Name:      p.ID,
		Facets:    Facets{"leapview": mustFacet(leapViewFacet(p))},
	}, nil
}

// MapPipelineJob is an alias with an action-oriented name for callers that
// prefer mapping functions over methods.
func MapPipelineJob(p Pipeline) (Job, error) { return JobForPipeline(p) }

// EventForPipelineRun maps a PipelineRun into one OpenLineage event.  Sources
// become inputs and the plan's materialized Model relations become outputs.
// A child Model run uses ParentRunID and ModelID to add the standard parent
// facet and emits only the explicitly supplied input/output lists.
func EventForPipelineRun(p Pipeline, r PipelineRun) (Event, error) {
	if err := p.validate(); err != nil {
		return Event{}, err
	}
	if err := r.validate(p); err != nil {
		return Event{}, err
	}
	job, err := JobForPipeline(p)
	if err != nil {
		return Event{}, err
	}
	eventType := r.EventType
	if eventType == "" {
		eventType = EventComplete
	}
	switch eventType {
	case EventStart, EventRunning, EventComplete, EventFail, EventAbort, EventOther:
	default:
		return Event{}, fmt.Errorf("unsupported OpenLineage event type %q", eventType)
	}
	eventTime := r.EventTime
	if eventTime.IsZero() {
		eventTime = time.Now().UTC()
	}
	eventTime = eventTime.UTC()

	runFacets := Facets{"leapview": mustFacet(leapViewFacet(p))}
	if r.NominalTime != nil {
		runFacets["nominalTime"] = mustFacet(nominalTimeFacet(*r.NominalTime))
	}
	if r.ParentRunID != "" {
		runFacets["parent"] = mustFacet(parentFacet(p, r.ParentRunID))
	}
	if r.TriggerID != "" || r.TriggerType != "" {
		runFacets["leapviewTrigger"] = mustFacet(map[string]any{
			"_producer": Producer, "_schemaURL": FacetSchemaURL,
			"triggerId": r.TriggerID, "triggerType": r.TriggerType,
		})
	}

	inputs := r.Inputs
	if inputs == nil && r.ParentRunID == "" {
		inputs = p.SourceInputs
	}
	outputs := r.Outputs
	if outputs == nil && r.ParentRunID == "" {
		outputs = p.MaterializationScope
	}
	return Event{
		EventType: eventType,
		EventTime: eventTime,
		Run:       Run{RunID: r.ID, Facets: runFacets},
		Job:       job,
		Inputs:    datasets(job.Namespace, inputs),
		Outputs:   datasets(job.Namespace, outputs),
		Producer:  Producer,
		SchemaURL: SchemaURL,
	}, nil
}

// MapPipelineRun is an alias with an action-oriented name.
func MapPipelineRun(p Pipeline, r PipelineRun) (Event, error) {
	return EventForPipelineRun(p, r)
}

// ModelRun returns a child Model execution event whose standard parent facet
// points at the PipelineRun.  The child job name is the Model relation while
// retaining the Pipeline namespace and scoped LeapView evidence.
func ModelRun(p Pipeline, r PipelineRun, modelID string) (Event, error) {
	r.ModelID = modelID
	if r.ParentRunID == "" {
		// The convenience form accepts the PipelineRun ID in r.ID and derives a
		// deterministic child ID.  Callers that already have a child ID should
		// set ParentRunID explicitly.
		parentID := r.ID
		r.ParentRunID = parentID
		r.ID = parentID + "/model/" + modelID
	}
	if r.Outputs == nil {
		r.Outputs = []string{modelID}
	}
	event, err := EventForPipelineRun(p, r)
	if err != nil {
		return Event{}, err
	}
	event.Job.Name = modelID
	event.Job.Facets = Facets{"leapview": mustFacet(leapViewFacet(p))}
	return event, nil
}

// MapModelRun is an alias for ModelRun.
func MapModelRun(p Pipeline, r PipelineRun, modelID string) (Event, error) {
	return ModelRun(p, r, modelID)
}

func (p Pipeline) validate() error {
	for label, value := range map[string]string{"pipeline id": p.ID} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", label)
		}
	}
	if strings.TrimSpace(p.Namespace) == "" && (strings.TrimSpace(p.ProjectID) == "" || strings.TrimSpace(p.Environment) == "") {
		return errors.New("namespace or project id and environment are required")
	}
	if p.Namespace == "" {
		p.Namespace = NamespaceFor(p.ProjectID, p.Environment)
	}
	return nil
}

func (r PipelineRun) validate(p Pipeline) error {
	if strings.TrimSpace(r.ID) == "" {
		return errors.New("run id is required")
	}
	if r.PipelineID != "" && r.PipelineID != p.ID {
		return fmt.Errorf("run pipeline id %q does not match pipeline %q", r.PipelineID, p.ID)
	}
	if r.ParentRunID != "" && r.ModelID == "" {
		return errors.New("model id is required for child run")
	}
	return nil
}

func datasets(namespace string, names []string) []Dataset {
	if len(names) == 0 {
		return nil
	}
	result := make([]Dataset, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, Dataset{Namespace: namespace, Name: name})
	}
	return result
}

func leapViewFacet(p Pipeline) map[string]any {
	facet := map[string]any{"_producer": Producer, "_schemaURL": FacetSchemaURL, "pipelineId": p.ID}
	for key, value := range map[string]string{
		"projectId": p.ProjectID, "environment": p.Environment,
		"semanticModelId": p.SemanticModelID, "generationId": p.GenerationID,
		"planDigest": p.PlanDigest, "selectionDigest": p.SelectionDigest,
		"executionDigest": p.ExecutionDigest, "provenanceDigest": p.ProvenanceDigest,
		"governanceDigest": p.GovernanceDigest, "evidenceDigest": p.EvidenceDigest,
	} {
		if strings.TrimSpace(value) != "" {
			facet[key] = value
		}
	}
	if len(p.QualificationChecks) > 0 {
		facet["qualificationChecks"] = append([]string(nil), p.QualificationChecks...)
	}
	return facet
}

func nominalTimeFacet(value time.Time) map[string]any {
	return map[string]any{
		"_producer": Producer, "_schemaURL": "https://openlineage.io/spec/facets/1-0-0/NominalTimeRunFacet.json",
		"nominalStartTime": value.UTC().Format(time.RFC3339Nano),
	}
}

func parentFacet(p Pipeline, runID string) map[string]any {
	namespace := p.Namespace
	if namespace == "" {
		namespace = NamespaceFor(p.ProjectID, p.Environment)
	}
	return map[string]any{
		"_producer": Producer, "_schemaURL": "https://openlineage.io/spec/facets/1-0-0/ParentRunFacet.json",
		"parent": map[string]any{
			"run": map[string]any{"runId": runID},
			"job": map[string]any{"namespace": namespace, "name": p.ID},
		},
	}
}

func mustFacet(value map[string]any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
