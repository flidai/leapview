//go:build linux

package hostinstall

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/google/uuid"
)

// This is a private checkpoint, not approval evidence. The candidate command
// produces normal durable publication/approval records; the host waits for
// those records to become active before asking the browser to validate them.
type transitionActivation struct {
	OperationID                 string `json:"operationId"`
	IntentDigest                string `json:"intentDigest"`
	PlanID                      string `json:"planId"`
	GenerationID                string `json:"generationId"`
	PublicationID               string `json:"publicationId"`
	PlanPolicySnapshotDigest    string `json:"planPolicySnapshotDigest"`
	ServingPolicySnapshotDigest string `json:"servingPolicySnapshotDigest"`
	Status                      string `json:"status"`
}

func (e *NativeEffects) transitionResultPath(rehearsal bool) string {
	if rehearsal || e.detached {
		return filepath.Join(e.operation, "transition-rehearsal-result.json")
	}
	return filepath.Join(e.operation, "transition-live-result.json")
}

func (e *NativeEffects) validateTransitionResult(result transitionActivation) error {
	if e.request.AccessTransition == nil {
		return errors.New("access transition result has no admitted intent")
	}
	plan, err := e.request.AccessTransition.Plan()
	if err != nil {
		return err
	}
	if result.OperationID != "access-transition:"+strings.TrimPrefix(e.id.ArtifactAdmissionDigest, "sha256:") || result.IntentDigest != plan.IntentDigest ||
		!digestPattern.MatchString(result.PlanPolicySnapshotDigest) || !digestPattern.MatchString(result.ServingPolicySnapshotDigest) ||
		(result.Status != "pending" && result.Status != "committed") {
		return errors.New("candidate access transition returned an unbound publication")
	}
	for _, value := range []string{result.PlanID, result.GenerationID, result.PublicationID} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil || parsed.String() != value {
			return errors.New("candidate access transition returned an invalid publication identity")
		}
	}
	if result.GenerationID == e.request.AccessTransition.ExpectedServingGeneration {
		return errors.New("access transition did not create a new serving generation")
	}
	return nil
}

func (e *NativeEffects) recordTransitionResult(raw string, rehearsal bool) error {
	var result transitionActivation
	if len(raw) > 65536 || json.Unmarshal([]byte(raw), &result) != nil {
		return errors.New("candidate access transition returned invalid output")
	}
	if err := e.validateTransitionResult(result); err != nil {
		return err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(e.transitionResultPath(rehearsal), encoded)
}

func (e *NativeEffects) waitTransitionActivation(ctx context.Context, postgres string, rehearsal bool) error {
	if e.request.AccessTransition == nil {
		return nil
	}
	raw, err := securefs.ReadPrivateFile(e.transitionResultPath(rehearsal))
	if err != nil {
		return err
	}
	var result transitionActivation
	if json.Unmarshal(raw, &result) != nil {
		return errors.New("invalid private transition publication checkpoint")
	}
	if err := e.validateTransitionResult(result); err != nil {
		return err
	}
	// UUIDs and digest are canonical above. Encode the arbitrary target as hex
	// rather than interpolate it as SQL text. This reads the authoritative
	// pointer and immutable plan; it cannot approve or activate a publication.
	target := hex.EncodeToString([]byte(e.request.AccessTransition.TargetID))
	project := hex.EncodeToString([]byte(e.request.AccessTransition.ProjectID))
	environment := hex.EncodeToString([]byte(e.request.AccessTransition.Environment))
	query := fmt.Sprintf(`SELECT EXISTS (
SELECT 1 FROM delivery.delivery_active_pointer a
JOIN delivery.delivery_publication p ON p.publication_id = a.publication_id
JOIN delivery.delivery_generation g ON g.generation_id = a.generation_id
JOIN delivery.delivery_plan n ON n.plan_id = g.plan_id
JOIN delivery.delivery_target t ON t.target_id = a.target_id
JOIN access.authorization_snapshot s ON s.project_id = t.project_id AND s.environment = t.environment AND s.generation_id = a.generation_id::text
WHERE a.target_id = convert_from(decode('%s','hex'),'UTF8')
AND a.generation_id = '%s'::uuid AND a.publication_id = '%s'::uuid
AND g.plan_id = '%s'::uuid AND t.project_id = convert_from(decode('%s','hex'),'UTF8')
AND t.environment = convert_from(decode('%s','hex'),'UTF8')
AND p.state = 'committed' AND p.target_id = a.target_id
AND p.generation_id = a.generation_id AND g.target_id = a.target_id
AND n.target_id = a.target_id AND n.plan_document->'authorization'->>'snapshotDigest' = '%s'
AND s.digest = '%s')`,
		target, result.GenerationID, result.PublicationID, result.PlanID, project, environment,
		result.PlanPolicySnapshotDigest, result.ServingPolicySnapshotDigest)
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	for {
		value, err := e.pgQuery(ctx, postgres, "leapview_control", query)
		if err != nil {
			return fmt.Errorf("read access transition activation: %w", err)
		}
		if strings.TrimSpace(value) == "t" {
			return nil
		}
		if strings.TrimSpace(value) != "f" {
			return errors.New("invalid access transition activation result")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("typed serving policy did not activate before validation: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}
