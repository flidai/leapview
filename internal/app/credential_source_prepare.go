package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type sourceCredentialOperationKey struct{}

func (a *sourceCredentialActivation) Prepare(ctx context.Context, actor string, resource credentialmodule.ValidationResource, request credentialmodule.ActivationRequest) (credentialmodule.ActivationRecord, error) {
	if err := a.AuthorizeMutation(ctx, actor, resource); err != nil {
		return credentialmodule.ActivationRecord{}, err
	}
	row, err := a.config.Credentials.GetActivationRequest(ctx, a.config.TargetID, request.OperationID)
	if err == nil {
		if row.Resource() != resource || row.Request != request || row.Receipt.ActorID != actor {
			return credentialmodule.ActivationRecord{}, credentialmodule.ErrValidationConflict
		}
		if row.State != "preparing" {
			return row.ActivationRecord(), nil
		}
	} else {
		if !errors.Is(err, credentialmodule.ErrValidationNotFound) {
			return credentialmodule.ActivationRecord{}, err
		}
		receipt, e := a.config.Credentials.ReadValidationReceipt(ctx, a.config.TargetID, request.ReceiptID)
		if e != nil {
			return credentialmodule.ActivationRecord{}, e
		}
		row = credentialmodule.ActivationRequestRecord{Receipt: receipt, Request: request}
		if row.Resource() != resource || receipt.ActorID != actor {
			return credentialmodule.ActivationRecord{}, credentialmodule.ErrValidationForbidden
		}
		// Persist intent before native source compilation, pool opening or build.
		err = a.transaction(ctx, func(tx pgx.Tx) error {
			var e error
			row, e = a.config.Credentials.ReserveActivationRequestTx(ctx, tx, receipt, request, a.auth(row, actor))
			return e
		})
		if err != nil {
			return row.ActivationRecord(), err
		}
	}
	return a.prepareNative(ctx, actor, row)
}
func (a *sourceCredentialActivation) prepareNative(ctx context.Context, actor string, row credentialmodule.ActivationRequestRecord) (credentialmodule.ActivationRecord, error) {
	fail := func(err error) (credentialmodule.ActivationRecord, error) { return row.ActivationRecord(), err }
	target, err := a.config.Delivery.Target(ctx, a.config.TargetID)
	if err != nil {
		return fail(err)
	}
	if target.ProjectID != row.Resource().ProjectID || target.Environment != row.Resource().Environment || target.ActiveGenerationID == "" || target.TargetRevision < 1 {
		return fail(credentialmodule.ErrValidationConflict)
	}
	generation, err := a.config.Reader.LoadGeneration(ctx, target.ActiveGenerationID)
	if err != nil {
		return fail(err)
	}
	stored, err := a.config.Reader.LoadPlan(ctx, generation.PlanID)
	if err != nil {
		return fail(err)
	}
	base, err := stored.RichPlan()
	if err != nil {
		return fail(err)
	}
	if generation.TargetID != target.TargetID || base.TargetID != target.TargetID || base.ProjectID.String() != target.ProjectID || base.Environment != target.Environment || base.Digest != generation.PlanDigest {
		return fail(credentialmodule.ErrValidationConflict)
	}
	sourceOwner := base.SourceOwnerID
	if sourceOwner == "" {
		sourceOwner = base.ActorID
	}
	operationCtx := context.WithValue(ctx, sourceCredentialOperationKey{}, row.Request.OperationID)
	plan, err := a.config.Mutations.CreatePlan(operationCtx, deploymentmodule.NativeDeliveryPlanRequest{ProjectID: projectgraph.ResourceID(target.ProjectID), TargetID: target.TargetID, Environment: target.Environment, PrincipalID: actor, SourceOwnerID: sourceOwner, Operation: string(deployment.DeliveryOperationRestatement), SourceDigest: base.SourceDigest, SourceAttestationDigest: base.Provenance.AttestationDigest, IdempotencyKey: "credential-plan-" + row.Request.OperationID})
	if err != nil {
		return fail(err)
	}
	if plan.ID == uuid.Nil || plan.ActorID != actor || plan.ProjectID.String() != target.ProjectID || plan.TargetID != target.TargetID || plan.Environment != target.Environment || plan.BaseGenerationID.String() != target.ActiveGenerationID || plan.BaseTargetRevision != target.TargetRevision || plan.SourceDigest != base.SourceDigest || plan.SourceAttestationDigest != base.Provenance.AttestationDigest || (row.PlanID != "" && row.PlanID != plan.ID.String()) {
		return fail(credentialmodule.ErrValidationConflict)
	}
	if err = a.config.Mutations.CompleteNativePlanCommand(operationCtx, plan); err != nil {
		return fail(err)
	}
	if row.PlanID == "" {
		err = a.transaction(ctx, func(tx pgx.Tx) error {
			next := row
			next.PlanID = plan.ID.String()
			var e error
			row, e = a.config.Credentials.TransitionActivationRequestTx(ctx, tx, row, next, actor, "", a.auth(row, actor))
			return e
		})
		if err != nil {
			return fail(err)
		}
	}
	build, err := a.config.Mutations.BuildPlan(operationCtx, deploymentmodule.NativeDeliveryBuildRequest{ProjectID: plan.ProjectID, TargetID: plan.TargetID, Environment: plan.Environment, PlanID: plan.ID, PrincipalID: actor, IdempotencyKey: "credential-build-" + row.Request.OperationID})
	if err != nil {
		return fail(err)
	}
	if build.PlanID != plan.ID || build.PlanDigest != plan.PlanDigest || build.CandidateID == uuid.Nil || build.ServingStateID == uuid.Nil || build.SealID == uuid.Nil || build.BaseGenerationID != plan.BaseGenerationID {
		return fail(credentialmodule.ErrValidationConflict)
	}
	if err = a.config.Mutations.CompleteNativeBuildCommand(operationCtx, build); err != nil {
		return fail(err)
	}
	// Completion verifies native build consequences. Re-read the exact immutable
	// generation; no caller-supplied candidate can become a credential operation.
	built, err := a.config.Reader.LoadGeneration(ctx, build.ServingStateID.String())
	if err != nil {
		return fail(err)
	}
	candidate, err := a.config.Reader.LoadCandidate(ctx, build.CandidateID.String())
	if err != nil {
		return fail(err)
	}
	if built.TargetID != target.TargetID || built.PlanID != plan.ID.String() || built.CandidateID != candidate.CandidateID || built.SnapshotSealID != build.SealID.String() || built.ServingArtifactDigest != build.ServingArtifactDigest || candidate.TargetID != target.TargetID || candidate.Status != "qualified" {
		return fail(credentialmodule.ErrValidationConflict)
	}
	publicationID := uuid.NewSHA1(uuid.MustParse(row.Request.OperationID), []byte("credential-publication")).String()
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(row.Request.OperationID+":"+plan.ID.String()+":"+build.CandidateID.String()+":"+build.ServingStateID.String())))
	err = a.transaction(ctx, func(tx pgx.Tx) error {
		auth := a.auth(row, actor)
		if err := auth(ctx, tx); err != nil {
			return err
		}
		current, err := a.config.Delivery.TargetForUpdateTx(ctx, tx, target.TargetID)
		if err != nil {
			return err
		}
		if current.TargetRevision != target.TargetRevision || current.ActiveGenerationID != target.ActiveGenerationID {
			return credentialmodule.ErrValidationConflict
		}
		if _, err = a.config.Delivery.CreatePublicationTx(ctx, tx, deploymentpostgres.PublicationInput{PublicationID: publicationID, TargetID: target.TargetID, GenerationID: built.GenerationID, ExpectedBaseGenerationID: target.ActiveGenerationID, CandidateID: candidate.CandidateID, SnapshotSealID: built.SnapshotSealID, ExpectedTargetRevision: target.TargetRevision, ActorID: actor, RequestDigest: digest}); err != nil {
			return err
		}
		preparation := credentialmodule.ActivationPreparation{OperationID: row.Request.OperationID, Receipt: row.Receipt, ExpectedTargetRevision: target.TargetRevision, PredecessorGenerationID: target.ActiveGenerationID, CandidateID: candidate.CandidateID, GenerationID: built.GenerationID, PublicationID: publicationID}
		if _, err = a.config.Credentials.PrepareActivationTx(ctx, tx, preparation, func(ctx context.Context, tx pgx.Tx, _ credentialmodule.ActivationPreparation) error {
			return auth(ctx, tx)
		}); err != nil {
			return err
		}
		next := row
		next.CandidateID = candidate.CandidateID
		next.GenerationID = built.GenerationID
		next.PublicationID = publicationID
		next.State = "prepared"
		row, err = a.config.Credentials.TransitionActivationRequestTx(ctx, tx, row, next, actor, "", auth)
		return err
	})
	return row.ActivationRecord(), err
}
