package adminpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
)

// AccessTransitionInventory is the read-only schema-32 baseline an operator
// binds into an admitted legacy-to-typed transition request.
type AccessTransitionInventory struct {
	TargetID                    string `json:"targetId"`
	Environment                 string `json:"environment"`
	ProjectID                   string `json:"projectId"`
	PolicyRevision              int64  `json:"expectedPolicyRevision"`
	PolicyDigest                string `json:"expectedPolicyDigest"`
	ServingGenerationID         string `json:"expectedServingGeneration"`
	ServingPolicySnapshotDigest string `json:"expectedServingPolicyDigest"`
}

// AccessTransitionInventory reads only existing target, policy-head and
// immutable serving-snapshot identities. Its SQL deliberately avoids columns
// introduced by candidate migrations so the qualified candidate can inspect a
// schema-32 restored copy before executing forward migrations.
func (o Operations) AccessTransitionInventory(ctx context.Context, projectID string, out io.Writer) error {
	if out == nil || strings.TrimSpace(projectID) == "" || projectID != strings.TrimSpace(projectID) {
		return errors.New("canonical project ID and output are required")
	}
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Production {
		return errors.New("access transition inventory is available only for a production installation")
	}
	configuration, err := accessConfigForAdmin(cfg)
	if err != nil {
		return err
	}
	pool, err := deps.OpenAccess(ctx, configuration)
	if err != nil {
		return err
	}
	defer pool.Close()
	bootstrap := platformbootstrap.New(pool)
	targetID, err := bootstrap.InstanceID(ctx)
	if err != nil {
		return err
	}
	environment, err := bootstrap.InstanceEnvironment(ctx)
	if err != nil {
		return err
	}
	claim, err := bootstrap.GetProjectClaim(ctx)
	if err != nil {
		return err
	}
	if claim.ProjectID != projectID || string(claim.Environment) != environment {
		return errors.New("requested project differs from the durable instance claim")
	}
	var policy AccessTransitionInventory
	policy.TargetID, policy.ProjectID, policy.Environment = targetID, projectID, environment
	if err := pool.QueryRow(ctx, `
		SELECT revision, digest
		FROM access.authorization_policy
		WHERE target_id = $1 AND project_id = $2 AND environment = $3`, targetID, projectID, environment).Scan(&policy.PolicyRevision, &policy.PolicyDigest); err != nil {
		return fmt.Errorf("read current access policy identity: %w", err)
	}
	if policy.PolicyRevision < 1 || !canonicalInventoryDigest(policy.PolicyDigest) {
		return errors.New("current access policy identity is invalid")
	}
	var activeGeneration string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(pointer.generation_id::text, '')
		FROM delivery.delivery_target target
		LEFT JOIN delivery.delivery_active_pointer pointer ON pointer.target_id = target.target_id
		WHERE target.target_id = $1 AND target.project_id = $2 AND target.environment = $3`, targetID, projectID, environment).Scan(&activeGeneration); err != nil {
		return fmt.Errorf("read current delivery generation: %w", err)
	}
	if activeGeneration == "" {
		return errors.New("an active serving generation is required for an access transition")
	}
	policy.ServingGenerationID = activeGeneration
	if err := pool.QueryRow(ctx, `
		SELECT digest
		FROM access.authorization_snapshot
		WHERE project_id = $1 AND environment = $2 AND generation_id = $3`, projectID, environment, activeGeneration).Scan(&policy.ServingPolicySnapshotDigest); err != nil {
		return fmt.Errorf("read current immutable serving-policy snapshot: %w", err)
	}
	if !canonicalInventoryDigest(policy.ServingPolicySnapshotDigest) {
		return errors.New("current immutable serving-policy snapshot digest is invalid")
	}
	if err := json.NewEncoder(out).Encode(policy); err != nil {
		return err
	}
	return nil
}

func canonicalInventoryDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range value[len("sha256:"):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
