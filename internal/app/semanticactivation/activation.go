package semanticactivation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/deployment"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	"github.com/flidai/leapview/internal/project/contractprojection"
	"github.com/flidai/leapview/internal/project/contractpublication"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	projectschema "github.com/flidai/leapview/internal/project/schema"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/strictjson"
)

var errSemanticActivationUnavailable = errors.New("semantic activation authority is unavailable")

type semanticActivationPublicationReader interface {
	ContractPublications(context.Context, string, projectgraph.ResourceID, projectgraph.Kind) ([]contractpublication.ContractPublication, error)
	ContractPublication(context.Context, string, projectgraph.ResourceID, projectgraph.Kind, string) (contractpublication.ContractPublication, error)
}

type semanticActivationAttributeReader interface {
	SemanticAttributeRegistry(context.Context) (access.SemanticAttributeRegistrySnapshot, error)
	SemanticAttributeControl(context.Context) (access.SemanticAttributeControlSnapshot, error)
	SemanticAttributeActivationAuthorityTx(context.Context, accesspostgres.Tx) (access.SemanticAttributeRegistrySnapshot, access.SemanticAttributeControlSnapshot, error)
}

type semanticActivationServingReader interface {
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
	ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
}

type semanticActivationDeliveryReader interface {
	Target(context.Context, string) (deploymentpostgres.DeliveryTarget, error)
	Generation(context.Context, string) (deploymentpostgres.DeliveryGeneration, error)
	Plan(context.Context, string) (deploymentpostgres.DeliveryPlan, error)
	IsRollbackPublicationTx(context.Context, deploymentpostgres.Tx, deploymentpostgres.DeliveryPublication) (bool, error)
}

type semanticActivationAuditRecorder interface {
	access.CanonicalAuditRecorder
	RecordCanonicalAuditEventTx(context.Context, accesspostgres.Tx, access.CanonicalAuditEvent) error
}

type transactionBoundAuditRecorder struct {
	recorder semanticActivationAuditRecorder
	tx       accesspostgres.Tx
}

func (r transactionBoundAuditRecorder) RecordCanonicalAuditEvent(ctx context.Context, event access.CanonicalAuditEvent) error {
	return r.recorder.RecordCanonicalAuditEventTx(ctx, r.tx, event)
}

type Fence struct {
	instanceID   string
	publications semanticActivationPublicationReader
	attributes   semanticActivationAttributeReader
	serving      semanticActivationServingReader
	delivery     semanticActivationDeliveryReader
	loader       projectbundle.ServingArtifactLoader
	audit        semanticActivationAuditRecorder
	clock        func() time.Time
}

type semanticActivationResolvedModel struct {
	id     projectgraph.ResourceID
	model  *semanticquery.CompiledModel
	source *semanticmodel.Model
	policy deployment.SemanticActivationModelEvidence
}

func New(instanceID string, publications semanticActivationPublicationReader, attributes semanticActivationAttributeReader, serving semanticActivationServingReader, delivery semanticActivationDeliveryReader, objects projectbundle.ArtifactObjectReader, audit semanticActivationAuditRecorder) (*Fence, error) {
	if instanceID == "" || publications == nil || attributes == nil || serving == nil || delivery == nil || objects == nil || audit == nil {
		return nil, errSemanticActivationUnavailable
	}
	return &Fence{instanceID: instanceID, publications: publications, attributes: attributes, serving: serving, delivery: delivery, loader: projectbundle.ServingArtifactLoader{Objects: objects}, audit: audit, clock: func() time.Time { return time.Now().UTC() }}, nil
}

// planEvidence selects the exact current publication and control authorities
// before the delivery plan is persisted. Deployment approval therefore binds
// the complete activation input rather than a mutable latest pointer.
func (f *Fence) PlanEvidence(ctx context.Context, artifact projectartifact.SourceBundle) (*deployment.SemanticActivationEvidence, error) {
	evidence, _, _, err := f.resolve(ctx, nil, artifact.Graph(), artifact.Manifest(), artifact.Digest(), f.clock().UTC(), false)
	return evidence, err
}

// ValidatePublication runs the semantic activation fence through the
// coordinator-owned transaction immediately before its target CAS. Mutable
// registry and control state is locked until that transaction completes.
func (f *Fence) ValidatePublication(ctx context.Context, tx deploymentpostgres.Tx, publication deploymentpostgres.DeliveryPublication) error {
	if f == nil || tx == nil || publication.GenerationID == "" || publication.ActorID == "" {
		return errSemanticActivationUnavailable
	}
	rollback, err := f.delivery.IsRollbackPublicationTx(ctx, tx, publication)
	if err != nil {
		return fmt.Errorf("resolve semantic activation rollback identity: %w", err)
	}
	return f.validate(ctx, tx, activationCutoverInput{GenerationID: publication.GenerationID, Actor: publication.ActorID, Rollback: rollback})
}

type activationCutoverInput struct {
	GenerationID string
	Actor        string
	Rollback     bool
}

func (f *Fence) validate(ctx context.Context, tx accesspostgres.Tx, input activationCutoverInput) error {
	generation, err := f.delivery.Generation(ctx, input.GenerationID)
	if err != nil {
		return fmt.Errorf("load semantic activation generation: %w", err)
	}
	target, err := f.delivery.Target(ctx, generation.TargetID)
	if err != nil {
		return fmt.Errorf("load semantic activation target: %w", err)
	}
	projectID, err := projectgraph.NewResourceID(target.ProjectID)
	if err != nil {
		return fmt.Errorf("load semantic activation project identity: %w", err)
	}
	planRow, err := f.delivery.Plan(ctx, generation.PlanID)
	if err != nil {
		return fmt.Errorf("load semantic activation plan: %w", err)
	}
	plan, err := planRow.RichPlan()
	if err != nil {
		return fmt.Errorf("decode semantic activation plan: %w", err)
	}
	state, err := f.serving.ByID(ctx, servingstate.ID(input.GenerationID))
	if err != nil {
		return fmt.Errorf("load semantic activation serving state: %w", err)
	}
	if err := validateServingActivationState(state); err != nil {
		return err
	}
	if target.TargetID != f.instanceID || generation.TargetID != target.TargetID || plan.TargetID != target.TargetID || plan.ProjectID != projectID || plan.Environment != target.Environment ||
		state.ProjectID != projectID || string(state.Environment) != target.Environment || string(state.ID) != input.GenerationID ||
		generation.PlanDigest != plan.Digest || generation.CompiledGraphDigest != planRow.CompiledGraphDigest || state.ProjectDigest == "" {
		return fmt.Errorf("%w: semantic activation serving identity differs", deployment.ErrDeliveryConflict)
	}
	if err := validateLegacyDataPolicyCutover(state.AccessPolicyJSON, input.Rollback); err != nil {
		return err
	}
	artifactRow, err := f.serving.ArtifactByServingState(ctx, servingstate.ID(input.GenerationID))
	if err != nil {
		return fmt.Errorf("load semantic activation artifact: %w", err)
	}
	compiled, err := f.loader.LoadCompiled(ctx, artifactRow, "")
	if err != nil {
		return fmt.Errorf("load semantic activation compiled artifact: %w", err)
	}
	if compiled.Graph.Digest() != generation.CompiledGraphDigest || compiled.BundleDigest != state.ProjectDigest || artifactRow.Digest != generation.ServingArtifactDigest {
		return fmt.Errorf("%w: semantic activation artifact digest differs", deployment.ErrDeliveryConflict)
	}
	now := f.clock().UTC()
	current, registry, resolved, err := f.resolve(ctx, tx, compiled.Graph, compiled.Manifest, compiled.BundleDigest, now, input.Rollback)
	if err != nil {
		return err
	}
	planned := plan.Evidence.SemanticActivation
	if err := validatePlannedActivationEvidence(planned, current); err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	for _, item := range resolved {
		policyIdentity, err := semanticquery.QualifySemanticAccessActivation(f.instanceID, item.id.String(), input.GenerationID, item.source, item.model, registry)
		if err != nil {
			return fmt.Errorf("compile activated semantic policy %s: %w", item.id, err)
		}
		if err := f.admitPolicyActivation(ctx, tx, projectID, target.Environment, input, *current, item, policyIdentity); err != nil {
			return err
		}
	}
	return nil
}

func (f *Fence) admitPolicyActivation(ctx context.Context, tx accesspostgres.Tx, projectID projectgraph.ResourceID, environment string, input activationCutoverInput, evidence deployment.SemanticActivationEvidence, item semanticActivationResolvedModel, policyIdentity semanticquery.SemanticAccessActivationPolicyIdentity) error {
	if err := validatePolicyDefinitionIdentity(item.policy.PolicyDefinitionDigest, policyIdentity); err != nil {
		return fmt.Errorf("activated semantic policy %s: %w", item.id, err)
	}
	return f.auditActivation(ctx, tx, projectID, environment, input, evidence, item, policyIdentity)
}

func validatePlannedActivationEvidence(planned, current *deployment.SemanticActivationEvidence) error {
	if current == nil {
		if planned != nil {
			return fmt.Errorf("%w: unprotected activation carries semantic authority evidence", deployment.ErrDeliveryConflict)
		}
		return nil
	}
	if planned == nil || !reflect.DeepEqual(*planned, *current) {
		return fmt.Errorf("%w: semantic activation evidence is stale or does not match the approved plan", deployment.ErrDeliveryStale)
	}
	return nil
}

func validatePolicyDefinitionIdentity(approved string, deployed semanticquery.SemanticAccessActivationPolicyIdentity) error {
	if approved == "" || deployed.DefinitionDigest == "" || deployed.GenerationDigest == "" || deployed.DefinitionDigest != approved {
		return fmt.Errorf("%w: deployed policy definition differs from approved evidence", deployment.ErrDeliveryStale)
	}
	return nil
}

func (f *Fence) auditActivation(ctx context.Context, tx accesspostgres.Tx, projectID projectgraph.ResourceID, environment string, input activationCutoverInput, evidence deployment.SemanticActivationEvidence, item semanticActivationResolvedModel, policyIdentity semanticquery.SemanticAccessActivationPolicyIdentity) error {
	metadata, err := json.Marshal(map[string]any{
		"activationEvidenceDigest": evidence.Digest,
		"publicationDigest":        item.policy.PublicationPolicy.Candidate.Digest,
		"policyDefinitionDigest":   policyIdentity.DefinitionDigest,
		"generationPolicyDigest":   policyIdentity.GenerationDigest,
		"registryDigest":           evidence.RegistryDigest,
		"registryRevision":         evidence.RegistryRevision,
		"controlDigest":            evidence.ControlDigest,
		"controlRevision":          evidence.ControlRevision,
		"rollback":                 input.Rollback,
	})
	if err != nil {
		return fmt.Errorf("encode semantic activation audit: %w", err)
	}
	resource, err := access.NewResourceRef(item.id, projectgraph.KindSemanticModel)
	if err != nil {
		return err
	}
	if tx == nil {
		return errSemanticActivationUnavailable
	}
	if err := access.PersistCanonicalAuditEvent(ctx, transactionBoundAuditRecorder{recorder: f.audit, tx: tx}, access.CanonicalAuditEvent{
		Identity:    projectgraph.ServingIdentity{ProjectID: projectID, Environment: environment, GenerationID: input.GenerationID},
		PrincipalID: input.Actor, Action: "semantic_access.activation_admitted", Resource: resource,
		Capability: access.CapabilityResourceManage, Status: "success", MetadataJSON: string(metadata),
	}); err != nil {
		return fmt.Errorf("persist semantic activation audit before cutover: %w", err)
	}
	return nil
}

func validateServingActivationState(state servingstate.State) error {
	if !state.CanActivate() {
		return fmt.Errorf("%w: semantic activation serving lifecycle state %q is not activatable", deployment.ErrDeliveryConflict, state.Status)
	}
	return nil
}

func (f *Fence) resolve(ctx context.Context, tx accesspostgres.Tx, graph projectgraph.ProjectGraph, manifest projectmanifest.ResourceManifest, bundleDigest string, now time.Time, historical bool) (*deployment.SemanticActivationEvidence, access.SemanticAttributeRegistrySnapshot, []semanticActivationResolvedModel, error) {
	references, err := contractprojection.NewReferenceContext(graph)
	if err != nil {
		return nil, access.SemanticAttributeRegistrySnapshot{}, nil, err
	}
	ids := make([]string, 0)
	for id, model := range manifest.SemanticModels {
		if semanticquery.ModelRequiresSemanticAccess(model) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, access.SemanticAttributeRegistrySnapshot{}, nil, nil
	}
	var registry access.SemanticAttributeRegistrySnapshot
	var control access.SemanticAttributeControlSnapshot
	if tx == nil {
		registry, control, err = f.stableAuthority(ctx)
	} else {
		registry, control, err = f.attributes.SemanticAttributeActivationAuthorityTx(ctx, tx)
	}
	if err != nil {
		return nil, access.SemanticAttributeRegistrySnapshot{}, nil, err
	}
	sort.Strings(ids)
	models := make([]deployment.SemanticActivationModelEvidence, 0, len(ids))
	resolved := make([]semanticActivationResolvedModel, 0, len(ids))
	for _, rawID := range ids {
		id, err := projectgraph.NewResourceID(rawID)
		if err != nil {
			return nil, access.SemanticAttributeRegistrySnapshot{}, nil, err
		}
		model := manifest.SemanticModels[rawID]
		compiled, err := semanticquery.CompileModel(model)
		if err != nil {
			return nil, access.SemanticAttributeRegistrySnapshot{}, nil, fmt.Errorf("compile semantic activation model %s: %w", id, err)
		}
		modelDigest, err := semanticquery.SemanticModelDigest(model)
		if err != nil {
			return nil, access.SemanticAttributeRegistrySnapshot{}, nil, err
		}
		publication, baseline, context, err := f.publication(ctx, manifest, references, id)
		if err != nil {
			return nil, access.SemanticAttributeRegistrySnapshot{}, nil, err
		}
		identity, err := projectmodule.PublicationPolicyIdentityFromContractPublication(publication, baseline)
		if err != nil {
			return nil, access.SemanticAttributeRegistrySnapshot{}, nil, err
		}
		admissionErr := validatePublicationForActivation(context, publication, now, historical)
		if admissionErr != nil {
			return nil, access.SemanticAttributeRegistrySnapshot{}, nil, fmt.Errorf("publication admission %s: %w", id, admissionErr)
		}
		policyIdentity, err := semanticquery.QualifySemanticAccessActivation(f.instanceID, rawID, "activation-plan:"+bundleDigest, model, compiled, registry)
		if err != nil {
			return nil, access.SemanticAttributeRegistrySnapshot{}, nil, fmt.Errorf("compile planned semantic policy %s: %w", id, err)
		}
		entry := deployment.SemanticActivationModelEvidence{ModelID: rawID, ModelDigest: modelDigest, PolicyDefinitionDigest: policyIdentity.DefinitionDigest, PublicationPolicy: identity}
		models = append(models, entry)
		resolved = append(resolved, semanticActivationResolvedModel{id: id, model: compiled, source: model, policy: entry})
	}
	evidence, err := deployment.NewSemanticActivationEvidence(deployment.SemanticActivationEvidence{
		InstanceID:      f.instanceID,
		RegistryProfile: registry.State.Profile, RegistryRevision: registry.State.Revision, RegistryDigest: registry.State.Digest,
		ControlProfile: control.State.Profile, ControlRevision: control.State.Revision, ControlDigest: control.State.Digest,
		BarrierProfile: deployment.SemanticBarrierProfile, ConsumerProfile: deployment.SemanticConsumerProfile,
		CacheProfile: deployment.SemanticCacheProfile, AuditProfile: deployment.SemanticAuditProfile, Models: models,
	})
	if err != nil {
		return nil, access.SemanticAttributeRegistrySnapshot{}, nil, err
	}
	return &evidence, registry, resolved, nil
}

func validatePublicationForActivation(context contractpublication.PolicyContext, publication contractpublication.ContractPublication, now time.Time, historical bool) error {
	if historical {
		return contractpublication.ValidateHistoricalPublication(context, publication)
	}
	return contractpublication.ValidateQualifiedPublication(context, publication, now)
}

func (f *Fence) publication(ctx context.Context, manifest projectmanifest.ResourceManifest, references contractprojection.ReferenceContext, id projectgraph.ResourceID) (contractpublication.ContractPublication, *contractpublication.ContractPublication, contractpublication.PolicyContext, error) {
	var authored projectcontracts.SemanticModel
	if err := projectschema.DecodeResource(projectschema.KindSemanticModel, "semantic-model.yaml", []byte(manifest.AuthoredResourceSources[id.String()]), &authored); err != nil {
		return contractpublication.ContractPublication{}, nil, contractpublication.PolicyContext{}, fmt.Errorf("decode semantic publication source %s: %w", id, err)
	}
	publications, err := f.publications.ContractPublications(ctx, f.instanceID, id, projectgraph.KindSemanticModel)
	if err != nil {
		return contractpublication.ContractPublication{}, nil, contractpublication.PolicyContext{}, fmt.Errorf("load semantic publication history %s: %w", id, err)
	}
	var matches []contractpublication.ContractPublication
	for _, candidate := range publications {
		projected, err := contractprojection.ProjectSemanticModel(authored, contractprojection.Contract{Version: candidate.Version, Compatibility: "backward"}, references)
		if err != nil {
			return contractpublication.ContractPublication{}, nil, contractpublication.PolicyContext{}, err
		}
		canonical, err := contractprojection.CanonicalBytes(projected)
		if err != nil {
			return contractpublication.ContractPublication{}, nil, contractpublication.PolicyContext{}, err
		}
		if bytes.Equal(canonical, candidate.CanonicalBytes) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return contractpublication.ContractPublication{}, nil, contractpublication.PolicyContext{}, fmt.Errorf("%w: semantic publication %s does not match candidate source", deployment.ErrDeliveryConflict, id)
	}
	publication := matches[0]
	policy := publication.Validation.PolicyEvidence
	if policy == nil {
		return contractpublication.ContractPublication{}, nil, contractpublication.PolicyContext{}, contractpublication.ErrInvalidPolicy
	}
	context := contractpublication.PolicyContext{BaselineKind: policy.BaselineKind}
	var baseline *contractpublication.ContractPublication
	if policy.BaselineKind == contractpublication.BaselineExisting {
		loaded, err := f.publications.ContractPublication(ctx, f.instanceID, id, projectgraph.KindSemanticModel, policy.Baseline.Version)
		if err != nil {
			return contractpublication.ContractPublication{}, nil, contractpublication.PolicyContext{}, fmt.Errorf("load semantic publication baseline %s: %w", id, err)
		}
		baseline = &loaded
		context.Existing = baseline
	}
	return publication, baseline, context, nil
}

func (f *Fence) stableAuthority(ctx context.Context) (access.SemanticAttributeRegistrySnapshot, access.SemanticAttributeControlSnapshot, error) {
	for attempt := 0; attempt < 2; attempt++ {
		registry, err := f.attributes.SemanticAttributeRegistry(ctx)
		if err != nil {
			return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, err
		}
		control, err := f.attributes.SemanticAttributeControl(ctx)
		if err != nil {
			return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, err
		}
		registryAgain, err := f.attributes.SemanticAttributeRegistry(ctx)
		if err != nil {
			return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, err
		}
		controlAgain, err := f.attributes.SemanticAttributeControl(ctx)
		if err != nil {
			return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, err
		}
		if registry.State == registryAgain.State && control.State == controlAgain.State {
			if err := access.ValidateSemanticAttributeRegistrySnapshot(registry); err != nil {
				return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, err
			}
			if err := access.ValidateSemanticAttributeControlSnapshot(control); err != nil {
				return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, err
			}
			return registry, control, nil
		}
	}
	return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, fmt.Errorf("%w: semantic registry/control state changed during activation", deployment.ErrDeliveryStale)
}

func legacyDataPolicyCount(raw string) (int, error) {
	if raw == "" {
		raw = "{}"
	}
	var policy projectmanifest.AccessPolicy
	if err := strictjson.DecodeWithOptions([]byte(raw), &policy, strictjson.Options{MaxBytes: 4 << 20}); err != nil {
		return 0, fmt.Errorf("%w: decode historical access policy: %v", deployment.ErrDeliveryInvalid, err)
	}
	return len(policy.DataPolicies), nil
}

func validateLegacyDataPolicyCutover(raw string, rollback bool) error {
	legacy, err := legacyDataPolicyCount(raw)
	if err != nil {
		return err
	}
	if legacy > 0 && !rollback {
		return fmt.Errorf("%w: new activation contains %d standalone DataPolicy resources; migrate representable row restrictions to SemanticModel access filters/grants and manually redesign masking or arbitrary expressions", deployment.ErrDeliveryInvalid, legacy)
	}
	return nil
}
