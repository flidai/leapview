package architecture

import (
	"strings"
	"testing"
)

func TestPolicyEvidencePlanningReusesProjectAuthorities(t *testing.T) {
	root := repoRoot(t)
	body := readArchitectureFixture(t, root, "internal/project/module/contract_policy_plan.go")
	for _, required := range []string{"graph.Dependencies(", "identitymodule.Resource", "publication.PolicyDecision()", "result.ValidatePublication()"} {
		if !strings.Contains(body, required) {
			t.Errorf("policy planning lost existing authority %q", required)
		}
	}
	for _, forbidden := range []string{`"github.com/flidai/leapview/internal/deployment"`, `"crypto/`, `"hash/`, `"database/sql"`, `"github.com/jackc/pgx`, "ocidigest.From", "sha256.Sum", "RequiresSecurityApproval = false", "func matchesGrant", "jsoncanonicalizer"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("policy planning introduced a separate authority or approval override: %s", forbidden)
		}
	}
}
