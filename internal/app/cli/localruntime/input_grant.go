package localruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

// DeclaredInputUploadGrantRequest carries only the exact fixture upload
// operation selected by the authenticated local authoring flow.
type DeclaredInputUploadGrantRequest struct {
	PrincipalID, ConnectionID, GrantID, OperationID string
	ExpectedRevision                                int64
}

// StageDeclaredInputUploadGrant uses the same installation-operator boundary
// as local initialization. It stages one exact upload grant, never expands a
// native session's authority, and still requires normal candidate admission.
// The caller resolves PrincipalID from the authenticated local session and
// ConnectionID from the validated declared-input plan before invoking it.
func (controller *Controller) StageDeclaredInputUploadGrant(ctx context.Context, expected State, request DeclaredInputUploadGrantRequest) error {
	if ctx == nil || expected.Reset != nil || expected.Authority.Environment != "dev" || expected.Authority.InstanceID == "" || expected.Session.SessionID == "" {
		return errors.New("declared input grant requires an established local development session")
	}
	operator := admincli.StageAccessGrantRequest{
		ProjectID: expected.Authority.ProjectUID, GrantID: request.GrantID, PrincipalID: request.PrincipalID,
		ResourceID: request.ConnectionID, ResourceKind: "connection", Actions: []string{"connection.upload"},
		ExpectedRevision: request.ExpectedRevision, OperationID: request.OperationID, Apply: true,
	}
	if _, err := operator.Grant(); err != nil {
		return fmt.Errorf("validate declared input upload grant: %w", err)
	}
	runtime, err := controller.lifecycleRuntime(ctx)
	if err != nil {
		return err
	}
	lock, err := acquireLifecycleLock(ctx, runtime.root)
	if err != nil {
		return err
	}
	defer lock.Release()
	runtime, err = controller.lifecycleRuntime(ctx)
	if err != nil {
		return err
	}
	current := runtime.state
	if current.Reset != nil || current.Phase != phaseReady || current.Status != statusApplied ||
		current.Checkout != expected.Checkout || current.Runtime != expected.Runtime || current.Endpoint != expected.Endpoint ||
		current.Authority != expected.Authority || current.Network != expected.Network || current.Session != expected.Session {
		return errors.New("declared input grant does not match the attached local runtime")
	}
	if err := controller.verifyResourceOwnership(ctx, current, false); err != nil {
		return err
	}
	if err := controller.requireInputGrantAttachment(ctx, runtime); err != nil {
		return err
	}
	output, err := controller.composeOutput(ctx, runtime.envPath, nil,
		"run", "--rm", "--no-deps", "leapview", "admin", "access", "stage-grant",
		"--project", operator.ProjectID, "--id", operator.GrantID, "--principal", operator.PrincipalID,
		"--resource", operator.ResourceID, "--kind", "connection", "--action", "connection.upload",
		"--expected-revision", strconv.FormatInt(operator.ExpectedRevision, 10), "--operation-id", operator.OperationID, "--apply",
	)
	if err != nil {
		return fmt.Errorf("stage declared input grant through local operator: %w", err)
	}
	var result struct {
		TargetID            string `json:"targetId"`
		ProjectID           string `json:"projectId"`
		Environment         string `json:"environment"`
		PolicyRevision      int64  `json:"policyRevision"`
		PolicyDigest        string `json:"policyDigest"`
		Applied             bool   `json:"applied"`
		RequiresPublication bool   `json:"requiresPublication"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("decode declared input grant evidence: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("declared input grant returned additional evidence")
	}
	if result.TargetID != current.Authority.InstanceID || result.ProjectID != current.Authority.ProjectUID || result.Environment != "dev" ||
		result.PolicyRevision != operator.ExpectedRevision+1 || platformdigest.ValidateSHA256Identity(result.PolicyDigest) != nil || !result.Applied || !result.RequiresPublication {
		return errors.New("declared input grant evidence does not match the exact local target policy")
	}
	return nil
}

func (controller *Controller) requireInputGrantAttachment(ctx context.Context, runtime lifecycleRuntime) error {
	lock, err := acquireAttachmentLock(ctx, runtime.root)
	if err != nil {
		return err
	}
	defer lock.Release()
	registry, err := loadAttachmentRegistry(filepath.Join(runtime.root, attachmentsFileName), attachmentBindingFor(runtime.state), false)
	if err != nil {
		return err
	}
	live, _, err := classifyAttachments(registry, controller.now(), controller.attachmentStaleAfter)
	if err != nil {
		return err
	}
	for _, record := range live {
		if record.PID == os.Getpid() {
			return nil
		}
	}
	return errors.New("declared input grant requires this process's live local runtime attachment")
}
