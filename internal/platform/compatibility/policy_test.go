package compatibility

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedReleaseTransitionPolicyValidates(t *testing.T) {
	policy, err := EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if policy.SchemaVersion != CurrentSchemaVersion || policy.PolicyVersion == "" {
		t.Fatalf("embedded policy identity = %#v", policy)
	}
	release, ok := policy.ReleaseByID("v0.1.0")
	if !ok || release.SourceRevision != "5bf4aded574df459e80d81b77d1989ecd4fa7de0" {
		t.Fatalf("v0.1.0 release = %#v, %v", release, ok)
	}
	if release.Artifacts[0].Image != ReleasedV010Image {
		t.Fatalf("v0.1.0 image = %q", release.Artifacts[0].Image)
	}
}

func TestReleaseTransitionPolicyRejectsInvalidDocuments(t *testing.T) {
	valid := testPolicyDocument(t)
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{name: "unknown schema", mutate: func(doc map[string]any) { doc["schemaVersion"] = 99 }, want: "schemaVersion"},
		{name: "missing source revision", mutate: func(doc map[string]any) {
			release := doc["releases"].([]any)[0].(map[string]any)
			delete(release, "sourceRevision")
		}, want: "sourceRevision"},
		{name: "mutable image", mutate: func(doc map[string]any) {
			artifact := doc["releases"].([]any)[0].(map[string]any)["artifacts"].([]any)[0].(map[string]any)
			artifact["image"] = "ghcr.io/flidai/leapview:latest"
		}, want: "image"},
		{name: "unsupported platform", mutate: func(doc map[string]any) {
			artifact := doc["releases"].([]any)[0].(map[string]any)["artifacts"].([]any)[0].(map[string]any)
			artifact["platform"] = "plan9/amd64"
		}, want: "platform"},
		{name: "duplicate release", mutate: func(doc map[string]any) {
			releases := doc["releases"].([]any)
			doc["releases"] = append(releases, releases[0])
		}, want: "duplicate release"},
		{name: "ambiguous transition", mutate: func(doc map[string]any) {
			transitions := doc["transitions"].([]any)
			doc["transitions"] = append(transitions, transitions[0])
		}, want: "duplicate transition"},
		{name: "unsupported operation", mutate: func(doc map[string]any) {
			transition := doc["transitions"].([]any)[0].(map[string]any)
			transition["operation"] = "reinstall"
		}, want: "operation"},
		{name: "unknown requirement", mutate: func(doc map[string]any) {
			transition := doc["transitions"].([]any)[0].(map[string]any)
			decision := transition["decision"].(map[string]any)
			decision["requirements"] = []any{"backup-before-mutation", "stopped-instance", "trust-me"}
		}, want: "unsupported requirement"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var doc map[string]any
			encoded, err := json.Marshal(valid)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &doc); err != nil {
				t.Fatal(err)
			}
			test.mutate(doc)
			encoded, err = json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParsePolicy(encoded); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParsePolicy() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEvaluateReleaseTransitionKeepsOperationsDistinct(t *testing.T) {
	policy, err := EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	v010, ok := policy.ReleaseByID("v0.1.0")
	if !ok {
		t.Fatal("embedded policy omits v0.1.0")
	}
	legacy := v010.IdentityForPlatform("linux/amd64")
	admitted, ok := policy.ReleaseByID(policy.CandidateRelease)
	if !ok {
		t.Fatalf("embedded policy omits candidate %q", policy.CandidateRelease)
	}
	candidate := ReleaseIdentity{
		Version: "0.2.0", SourceRevision: strings.Repeat("a", 40),
		Image:        "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64),
		Distribution: "public", Platform: "linux/amd64",
	}

	fresh := policy.Evaluate(Request{Operation: OperationFreshInstall, Next: admitted.IdentityForPlatform("linux/amd64")})
	if !fresh.Allowed || fresh.ReasonCode != ReasonAllowedFreshInstall {
		t.Fatalf("fresh-install decision = %#v", fresh)
	}
	upgrade := policy.Evaluate(Request{Operation: OperationUpgrade, Current: legacy, Next: candidate})
	if upgrade.Allowed || upgrade.ReasonCode != ReasonDeniedUnknownRelease {
		t.Fatalf("upgrade decision = %#v", upgrade)
	}
	rollback := policy.Evaluate(Request{Operation: OperationRollback, Current: candidate, Next: legacy})
	if rollback.Allowed || rollback.ReasonCode != ReasonDeniedUnknownRelease {
		t.Fatalf("rollback decision = %#v", rollback)
	}
}

func TestEvaluateReleaseTransitionRequiresBothExactEndpoints(t *testing.T) {
	policy, err := ParsePolicy(mustPolicyJSON(t, testPolicyDocument(t)))
	if err != nil {
		t.Fatal(err)
	}
	from, _ := policy.ReleaseByID("v1.0.0")
	to, _ := policy.ReleaseByID("v1.1.0")
	for _, test := range []struct {
		name    string
		current ReleaseIdentity
		next    ReleaseIdentity
	}{
		{name: "unknown source", current: ReleaseIdentity{Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("e", 64), Platform: "linux/amd64"}, next: to.IdentityForPlatform("linux/amd64")},
		{name: "unknown target", current: from.IdentityForPlatform("linux/amd64"), next: ReleaseIdentity{Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("e", 64), Platform: "linux/amd64"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision := policy.Evaluate(Request{Operation: OperationUpgrade, Current: test.current, Next: test.next})
			if decision.Allowed || decision.ReasonCode != ReasonDeniedUnknownRelease {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
}

func TestBindCandidateUsesAdmittedImageIdentity(t *testing.T) {
	base, err := EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("e", 64)
	bound, err := base.BindCandidate(ReleaseIdentity{
		Version: "0.3.0", SourceRevision: strings.Repeat("d", 40), Image: image, Distribution: "public",
	}, []string{"linux/amd64", "linux/arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if bound.CandidateRelease != "v0.3.0" {
		t.Fatalf("candidate release = %q", bound.CandidateRelease)
	}
	release, ok := bound.ReleaseByID("v0.3.0")
	if !ok || release.IdentityForPlatform("linux/arm64").Image != image {
		t.Fatalf("bound release = %#v, %v", release, ok)
	}
	if _, err := ParsePolicy(mustPolicyJSON(t, bound)); err != nil {
		t.Fatalf("bound policy does not round trip: %v", err)
	}
}

func TestBindCandidateMaterializesReviewedUpgradeAndRollback(t *testing.T) {
	base, err := EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("e", 64)
	bound, err := base.BindCandidate(ReleaseIdentity{
		Version: "0.3.0", SourceRevision: strings.Repeat("d", 40), Image: image, Distribution: "public",
	}, []string{"linux/amd64", "linux/arm64"})
	if err != nil {
		t.Fatal(err)
	}
	previous, ok := bound.ReleaseByID("v0.2.0-rc.1")
	if !ok {
		t.Fatal("reviewed predecessor is absent")
	}
	candidate, ok := bound.ReleaseByID("v0.3.0")
	if !ok {
		t.Fatal("bound candidate is absent")
	}
	for _, test := range []struct {
		operation Operation
		current   ReleaseIdentity
		next      ReleaseIdentity
	}{
		{operation: OperationUpgrade, current: previous.IdentityForPlatform("linux/amd64"), next: candidate.IdentityForPlatform("linux/amd64")},
		{operation: OperationRollback, current: candidate.IdentityForPlatform("linux/amd64"), next: previous.IdentityForPlatform("linux/amd64")},
	} {
		decision := bound.Evaluate(Request{Operation: test.operation, Current: test.current, Next: test.next})
		if err := decision.Err(); err != nil {
			t.Fatalf("%s decision = %#v: %v", test.operation, decision, err)
		}
	}
}

func TestBindCandidateRejectsMissingReviewedPredecessor(t *testing.T) {
	base, err := EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	base.Releases = base.Releases[:1]
	base.CandidateRelease = base.Releases[0].ID
	_, err = base.BindCandidate(ReleaseIdentity{
		Version: "0.3.0", SourceRevision: strings.Repeat("d", 40),
		Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("e", 64), Distribution: "public",
	}, []string{"linux/amd64", "linux/arm64"})
	if err == nil || !strings.Contains(err.Error(), "predecessor") {
		t.Fatalf("missing predecessor error = %v", err)
	}
}

func TestEvaluateReleaseTransitionRequiresExactExplicitPair(t *testing.T) {
	policy, err := ParsePolicy(mustPolicyJSON(t, testPolicyDocument(t)))
	if err != nil {
		t.Fatal(err)
	}
	from, _ := policy.ReleaseByID("v1.0.0")
	to, _ := policy.ReleaseByID("v1.1.0")
	allowed := policy.Evaluate(Request{
		Operation: OperationUpgrade,
		Current:   from.IdentityForPlatform("linux/amd64"),
		Next:      to.IdentityForPlatform("linux/amd64"),
	})
	if !allowed.Allowed || allowed.ReasonCode != ReasonAllowedExplicitTransition {
		t.Fatalf("explicit transition decision = %#v", allowed)
	}

	wrong := to.IdentityForPlatform("linux/amd64")
	wrong.Image = "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("f", 64)
	denied := policy.Evaluate(Request{Operation: OperationUpgrade, Current: from.IdentityForPlatform("linux/amd64"), Next: wrong})
	if denied.Allowed || denied.ReasonCode != ReasonDeniedUnknownRelease {
		t.Fatalf("substituted candidate decision = %#v", denied)
	}
}

func TestRejectLegacyStateRecognizesSQLiteOptionsWithoutMutation(t *testing.T) {
	home := t.TempDir()
	legacyPath := filepath.Join(home, LegacyV010Database)
	if err := os.WriteFile(legacyPath, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	currentPath := filepath.Join(home, "leapview.db") + "?_pragma=busy_timeout(5000)"

	err := RejectLegacyState(currentPath)
	if !errors.Is(err, ErrV010FreshInstallOnly) {
		t.Fatalf("RejectLegacyState() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "leapview.db")); !os.IsNotExist(err) {
		t.Fatalf("compatibility check created current database: %v", err)
	}
}

func TestValidateUpgradeImagesRejectsUnknownEndpointInEitherDirection(t *testing.T) {
	current := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		name    string
		current string
		next    string
	}{
		{name: "source", current: ReleasedV010Image, next: current},
		{name: "target", current: current, next: ReleasedV010Image},
	} {
		t.Run(test.name, func(t *testing.T) {
			var decisionErr *DecisionError
			err := ValidateUpgradeImages(test.current, test.next)
			if !errors.As(err, &decisionErr) || decisionErr.Decision.ReasonCode != ReasonDeniedUnknownRelease {
				t.Fatalf("ValidateUpgradeImages() error = %v", err)
			}
		})
	}
}

func testPolicyDocument(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"schemaVersion":    1,
		"policyVersion":    "ubdr/v1",
		"candidateRelease": "v1.1.0",
		"releases": []any{
			testReleaseDocument("v1.0.0", "1.0.0", "a", "b"),
			testReleaseDocument("v1.1.0", "1.1.0", "c", "d"),
		},
		"transitions": []any{map[string]any{
			"operation": "upgrade", "from": "v1.0.0", "to": "v1.1.0",
			"platforms": []any{"linux/amd64"},
			"decision": map[string]any{
				"allowed": true, "reasonCode": "transition.allowed.explicit",
				"remediation": "", "requirements": []any{"backup-before-mutation", "stopped-instance"},
			},
		}},
	}
}

func testReleaseDocument(id, version, revision, digest string) map[string]any {
	return map[string]any{
		"id": id, "version": version, "sourceRevision": strings.Repeat(revision, 40),
		"distribution": "public", "legacyMarkers": []any{},
		"artifacts": []any{map[string]any{
			"platform": "linux/amd64",
			"image":    "ghcr.io/flidai/leapview@sha256:" + strings.Repeat(digest, 64),
		}},
		"defaults": map[string]any{
			"freshInstall": map[string]any{
				"allowed": true, "reasonCode": "transition.allowed.fresh_install", "remediation": "", "requirements": []any{},
			},
			"upgrade": map[string]any{
				"allowed": false, "reasonCode": "transition.denied.no_explicit_rule", "remediation": "use an explicitly supported transition", "requirements": []any{},
			},
			"rollback": map[string]any{
				"allowed": false, "reasonCode": "transition.denied.no_explicit_rule", "remediation": "use an explicitly supported transition", "requirements": []any{},
			},
		},
	}
}

func mustPolicyJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
