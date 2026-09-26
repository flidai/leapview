package access

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

const maxDurableGrantTTL = 365 * 24 * time.Hour

func validateGrantPermissions(pairs []PermissionPair, target DurableGrantTarget, execution bool) error {
	if err := ValidatePermissionPairs(pairs); err != nil {
		return fmt.Errorf("%w: permission pairs: %v", ErrInvalidDurableGrant, err)
	}
	if len(pairs) == 0 {
		return fmt.Errorf("%w: at least one permission pair is required", ErrInvalidDurableGrant)
	}
	for _, pair := range pairs {
		if !execution && !target.pairTargetMatches(pair) {
			return fmt.Errorf("%w: every grant pair must bind the exact target", ErrInvalidDurableGrant)
		}
		if execution && (pair.Target.Scope != PermissionScopeResource || pair.Target.ProjectID != target.ProjectID || pair.Target.IncludeFuture) {
			return fmt.Errorf("%w: execution pairs must be exact resources in the target project", ErrInvalidDurableGrant)
		}
		definition, ok := Permission(pair.Action)
		if !ok {
			return fmt.Errorf("%w: unknown action %q", ErrInvalidDurableGrant, pair.Action)
		}
		if !definition.Delegable && !(execution && pair.Action == ActionPipelineRun) {
			return fmt.Errorf("%w: action %q is not delegable", ErrGrantPermissionCeiling, pair.Action)
		}
	}
	return nil
}

func validateIssuancePermissions(authority []PermissionPair, target DurableGrantTarget, kind GrantKind, requested []PermissionPair) error {
	if authority == nil {
		return fmt.Errorf("%w: explicit current issuance authority is required", ErrGrantPermissionCeiling)
	}
	if err := ValidatePermissionPairs(authority); err != nil {
		return fmt.Errorf("%w: issuance authority: %v", ErrGrantPermissionCeiling, err)
	}
	for _, pair := range requested {
		if !PermissionSetAllows(authority, pair) {
			return fmt.Errorf("%w: requested action %q is outside current issuer authority", ErrGrantPermissionCeiling, pair.Action)
		}
	}
	if kind == DurableGrantKindAdminEnvelope {
		manage, err := NewProjectPermissionPair(ActionProjectAccessManage, target.ProjectID)
		if err != nil || !PermissionSetAllows(authority, manage) {
			return fmt.Errorf("%w: project.access.manage is required for envelope issuance", ErrGrantPermissionCeiling)
		}
		delegate, err := NewProjectPermissionPair(ActionProjectAccessDelegate, target.ProjectID)
		if err != nil || !PermissionSetAllows(authority, delegate) {
			return fmt.Errorf("%w: project.access.delegate is required for envelope issuance", ErrGrantPermissionCeiling)
		}
		return nil
	}
	resource, err := NewResourceRef(target.ResourceID, target.ResourceKind)
	if err != nil {
		return err
	}
	action := ActionResourceShare
	if kind == DurableGrantKindExecution {
		action = ActionWorkloadDelegate
	}
	required, err := NewExactPermissionPair(action, target.ProjectID, resource)
	if err != nil || !PermissionSetAllows(authority, required) {
		return fmt.Errorf("%w: required issuance action %q is unavailable", ErrGrantPermissionCeiling, action)
	}
	return nil
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func durableIdentity(value string, max int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= max && strings.IndexFunc(value, unicode.IsControl) < 0
}

func durableFingerprint(value string) bool {
	if !durableIdentity(value, 512) {
		return false
	}
	// Accept either the native 64-hex credential fingerprint or a test/adapter
	// opaque evidence ID. Neither form can be used as a bearer credential.
	return len(value) >= 16
}

func durableDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil
}

func durableResourceKind(kind projectgraph.Kind) bool {
	switch kind {
	case projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindDashboard:
		return true
	default:
		return false
	}
}
