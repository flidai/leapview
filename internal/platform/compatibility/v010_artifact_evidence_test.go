package compatibility

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

var validV010ResolvedArtifact = V010ResolvedArtifact{
	Image:                  ReleasedV010Image,
	ResolvedDigest:         "sha256:677caaf256cb3a0d61efd47b289debbd91984976a5a5c4b372196a5d79ce7153",
	Platform:               ReleasedV010Platform,
	PlatformManifestDigest: ReleasedV010PlatformManifest,
	ConfigDigest:           ReleasedV010ConfigDigest,
	Authenticated:          true,
	SourceRepository:       ReleasedV010SourceRepository,
	SourceTag:              ReleasedV010ID,
	Version:                "0.1.0",
	SourceRevision:         "5bf4aded574df459e80d81b77d1989ecd4fa7de0",
}

type recordingV010Resolver struct {
	resolved V010ResolvedArtifact
	err      error
	requests []V010ArtifactResolutionRequest
}

func (r *recordingV010Resolver) ResolveExact(_ context.Context, request V010ArtifactResolutionRequest) (V010ResolvedArtifact, error) {
	r.requests = append(r.requests, request)
	if r.err != nil {
		return V010ResolvedArtifact{}, r.err
	}
	return r.resolved, nil
}

func TestVerifyReleasedV010ArtifactUsesOnlyExactPolicyIdentity(t *testing.T) {
	resolver := &recordingV010Resolver{resolved: validV010ResolvedArtifact}
	verifiedAt := time.Date(2026, time.July, 13, 15, 45, 27, 0, time.UTC)
	evidence, err := VerifyReleasedV010Artifact(context.Background(), V010ArtifactVerificationOptions{
		PolicyDocument: EmbeddedPolicyDocument(),
		Resolver:       resolver,
		Now:            func() time.Time { return verifiedAt },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolver.requests) != 1 {
		t.Fatalf("registry resolution count = %d, want 1", len(resolver.requests))
	}
	request := resolver.requests[0]
	if request.Image != ReleasedV010Image || request.Platform != ReleasedV010Platform || !request.RequireAuthentication {
		t.Fatalf("registry request = %#v", request)
	}
	if evidence.Identity.Image != ReleasedV010Image || evidence.Artifact.ResolvedDigest != validV010ResolvedArtifact.ResolvedDigest ||
		evidence.Provenance.SourceRevision != validV010ResolvedArtifact.SourceRevision || evidence.VerifiedAt != verifiedAt {
		t.Fatalf("release identity evidence = %#v", evidence)
	}
	document, err := MarshalV010ReleaseIdentityEvidence(evidence, EmbeddedPolicyDocument())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateV010ReleaseIdentityEvidence(document, EmbeddedPolicyDocument()); err != nil {
		t.Fatalf("produced evidence did not satisfy owner validator: %v", err)
	}
}

func TestVerifyReleasedV010ArtifactAcceptsCandidateBoundPolicyWithoutChangingLegacyIdentity(t *testing.T) {
	base, err := EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	bound, err := base.BindCandidate(ReleaseIdentity{
		Version: "0.3.0", SourceRevision: strings.Repeat("c", 40),
		Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("d", 64), Distribution: "public",
	}, []string{"linux/amd64", "linux/arm64"})
	if err != nil {
		t.Fatal(err)
	}
	document, err := MarshalPolicy(bound)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &recordingV010Resolver{resolved: validV010ResolvedArtifact}
	if _, err := VerifyReleasedV010Artifact(context.Background(), V010ArtifactVerificationOptions{
		PolicyDocument: document, Resolver: resolver,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyReleasedV010ArtifactFailsClosedWithoutRegistryProof(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "missing credentials", err: errors.New("registry credentials unavailable")},
		{name: "missing artifact", err: errors.New("registry manifest not found")},
		{name: "registry unavailable", err: errors.New("registry request timed out")},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := &recordingV010Resolver{err: test.err}
			_, err := VerifyReleasedV010Artifact(context.Background(), V010ArtifactVerificationOptions{
				PolicyDocument: EmbeddedPolicyDocument(), Resolver: resolver,
			})
			if err == nil || !strings.Contains(err.Error(), test.err.Error()) {
				t.Fatalf("verification error = %v, want %q", err, test.err)
			}
			if len(resolver.requests) != 1 || resolver.requests[0].Image != ReleasedV010Image {
				t.Fatalf("resolver attempted unexpected fallback: %#v", resolver.requests)
			}
		})
	}
}

func TestVerifyReleasedV010ArtifactRejectsRegistryAndProvenanceMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*V010ResolvedArtifact)
		want   string
	}{
		{name: "alternate namespace", mutate: func(value *V010ResolvedArtifact) {
			value.Image = "ghcr.io/flidai/libredash@sha256:" + strings.Repeat("6", 64)
		}, want: "exact policy-declared"},
		{name: "mutable digest mismatch", mutate: func(value *V010ResolvedArtifact) { value.ResolvedDigest = "sha256:" + strings.Repeat("6", 64) }, want: "exact policy-declared"},
		{name: "wrong platform", mutate: func(value *V010ResolvedArtifact) { value.Platform = "linux/arm64" }, want: "linux/amd64"},
		{name: "unauthenticated", mutate: func(value *V010ResolvedArtifact) { value.Authenticated = false }, want: "registry credentials"},
		{name: "wrong source", mutate: func(value *V010ResolvedArtifact) { value.SourceRepository = "https://github.com/flidai/leapview" }, want: "OCI provenance"},
		{name: "wrong tag", mutate: func(value *V010ResolvedArtifact) { value.SourceTag = "v0.1" }, want: "OCI provenance"},
		{name: "wrong version", mutate: func(value *V010ResolvedArtifact) { value.Version = "0.1.1" }, want: "OCI provenance"},
		{name: "wrong revision", mutate: func(value *V010ResolvedArtifact) { value.SourceRevision = strings.Repeat("a", 40) }, want: "OCI provenance"},
		{name: "invalid platform manifest digest", mutate: func(value *V010ResolvedArtifact) { value.PlatformManifestDigest = "not-a-digest" }, want: "platform manifest digest"},
		{name: "invalid config digest", mutate: func(value *V010ResolvedArtifact) { value.ConfigDigest = "sha256:" + strings.Repeat("A", 64) }, want: "config digest"},
		{name: "different platform manifest", mutate: func(value *V010ResolvedArtifact) { value.PlatformManifestDigest = "sha256:" + strings.Repeat("1", 64) }, want: "reviewed immutable OCI graph"},
		{name: "different config", mutate: func(value *V010ResolvedArtifact) { value.ConfigDigest = "sha256:" + strings.Repeat("2", 64) }, want: "reviewed immutable OCI graph"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved := validV010ResolvedArtifact
			test.mutate(&resolved)
			resolver := &recordingV010Resolver{resolved: resolved}
			_, err := VerifyReleasedV010Artifact(context.Background(), V010ArtifactVerificationOptions{
				PolicyDocument: EmbeddedPolicyDocument(), Resolver: resolver,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("verification error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestVerifyReleasedV010ArtifactRejectsPolicySubstitutionBeforeRegistryAccess(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Release)
	}{
		{name: "distribution", mutate: func(release *Release) { release.Distribution = "public" }},
		{name: "source revision", mutate: func(release *Release) { release.SourceRevision = strings.Repeat("a", 40) }},
		{name: "registry identity", mutate: func(release *Release) {
			release.Artifacts[0].Image = "ghcr.io/flidai/libredash@sha256:" + strings.Repeat("6", 64)
		}},
		{name: "platform", mutate: func(release *Release) { release.Artifacts[0].Platform = "linux/arm64" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, err := EmbeddedPolicy()
			if err != nil {
				t.Fatal(err)
			}
			for index := range policy.Releases {
				if policy.Releases[index].ID == ReleasedV010ID {
					test.mutate(&policy.Releases[index])
				}
			}
			document, err := MarshalPolicy(policy)
			if err != nil {
				t.Fatal(err)
			}
			resolver := &recordingV010Resolver{resolved: validV010ResolvedArtifact}
			_, err = VerifyReleasedV010Artifact(context.Background(), V010ArtifactVerificationOptions{
				PolicyDocument: document, Resolver: resolver,
			})
			if err == nil || !strings.Contains(err.Error(), "checked-in reviewed release") {
				t.Fatalf("verification error = %v", err)
			}
			if len(resolver.requests) != 0 {
				t.Fatalf("registry was accessed for substituted policy: %#v", resolver.requests)
			}
		})
	}
}

func TestValidateV010ReleaseIdentityEvidenceFixture(t *testing.T) {
	for _, path := range []string{
		"testdata/v010-release-identity.valid.json",
		"testdata/v010-release-identity.executed.valid.json",
	} {
		t.Run(path, func(t *testing.T) {
			document, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			policyDocument := EmbeddedPolicyDocument()
			if strings.Contains(path, ".executed.") {
				policyDocument = v010CandidateBoundEvidencePolicy(t)
			}
			evidence, err := ValidateV010ReleaseIdentityEvidence(document, policyDocument)
			if err != nil {
				t.Fatal(err)
			}
			if evidence.Artifact.PlatformManifestDigest != validV010ResolvedArtifact.PlatformManifestDigest ||
				evidence.Artifact.ConfigDigest != validV010ResolvedArtifact.ConfigDigest {
				t.Fatalf("fixture does not contain the observed released artifact digests: %#v", evidence.Artifact)
			}
		})
	}
}

func TestValidateV010ReleaseIdentityEvidenceRejectsInvalidDocuments(t *testing.T) {
	valid, err := os.ReadFile("testdata/v010-release-identity.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{name: "unknown field", mutate: func(value map[string]any) { value["fallbackImage"] = "libredash:local" }, want: "schema"},
		{name: "policy digest mismatch", mutate: func(value map[string]any) { value["policySha256"] = strings.Repeat("0", 64) }, want: "supplied policy"},
		{name: "alternate identity", mutate: func(value map[string]any) { value["identity"].(map[string]any)["distribution"] = "public" }, want: "schema"},
		{name: "unauthenticated artifact", mutate: func(value map[string]any) { value["artifact"].(map[string]any)["authenticated"] = false }, want: "schema"},
		{name: "wrong source tag", mutate: func(value map[string]any) { value["provenance"].(map[string]any)["sourceTag"] = "candidate" }, want: "schema"},
		{name: "invalid config digest", mutate: func(value map[string]any) { value["artifact"].(map[string]any)["configDigest"] = "sha256:bad" }, want: "schema"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(valid, &value); err != nil {
				t.Fatal(err)
			}
			test.mutate(value)
			document, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ValidateV010ReleaseIdentityEvidence(document, EmbeddedPolicyDocument())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}

	trailing := append(append([]byte(nil), valid...), []byte("\n{}")...)
	if _, err := ValidateV010ReleaseIdentityEvidence(trailing, EmbeddedPolicyDocument()); err == nil {
		t.Fatal("trailing evidence document was accepted")
	}
}

func TestValidateV010ReleaseIdentityEvidenceRejectsForgedExecution(t *testing.T) {
	valid, err := os.ReadFile("testdata/v010-release-identity.executed.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "wrong image identity", mutate: func(execution map[string]any) { execution["imageId"] = "sha256:" + strings.Repeat("1", 64) }},
		{name: "unclean shutdown", mutate: func(execution map[string]any) { execution["cleanShutdown"] = false }},
		{name: "cleanup not verified", mutate: func(execution map[string]any) { execution["cleanupVerified"] = false }},
		{name: "impossible chronology", mutate: func(execution map[string]any) { execution["stoppedAt"] = "2026-07-13T15:45:26Z" }},
		{name: "resource identity mismatch", mutate: func(execution map[string]any) {
			execution["networkName"] = "leapview-v010-" + strings.Repeat("f", 32) + "-network"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(valid, &value); err != nil {
				t.Fatal(err)
			}
			test.mutate(value["execution"].(map[string]any))
			document, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateV010ReleaseIdentityEvidence(document, v010CandidateBoundEvidencePolicy(t)); err == nil {
				t.Fatal("forged execution evidence was accepted")
			}
		})
	}
}

func TestValidateV010ReleaseIdentityEvidenceRejectsForgedApplicationJourney(t *testing.T) {
	valid, err := os.ReadFile("testdata/v010-release-identity.executed.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "authentication not verified", mutate: func(journey map[string]any) { journey["authenticationVerified"] = false }},
		{name: "wrong managed data", mutate: func(journey map[string]any) { journey["managedDataRows"] = 2.0 }},
		{name: "semantic result mismatch", mutate: func(journey map[string]any) { journey["semanticResultSha256"] = strings.Repeat("0", 64) }},
		{name: "dashboard result mismatch", mutate: func(journey map[string]any) { journey["dashboardResultSha256"] = strings.Repeat("0", 64) }},
		{name: "completion before readiness", mutate: func(journey map[string]any) { journey["completedAt"] = "2026-07-13T15:45:27Z" }},
		{name: "credential field", mutate: func(journey map[string]any) { journey["temporaryPassword"] = "secret" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(valid, &value); err != nil {
				t.Fatal(err)
			}
			journey := value["execution"].(map[string]any)["journey"].(map[string]any)
			test.mutate(journey)
			document, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateV010ReleaseIdentityEvidence(document, v010CandidateBoundEvidencePolicy(t)); err == nil {
				t.Fatal("forged application journey evidence was accepted")
			}
		})
	}
}

func TestValidateV010ReleaseIdentityEvidenceRejectsForgedStoppedStateInventory(t *testing.T) {
	valid, err := os.ReadFile("testdata/v010-release-identity.executed.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		rehash bool
	}{
		{name: "missing principal", mutate: func(inventory map[string]any) {
			inventory["principals"] = inventory["principals"].([]any)[:1]
		}, rehash: true},
		{name: "altered project", mutate: func(inventory map[string]any) {
			inventory["project"].(map[string]any)["title"] = "Forged Project"
		}, rehash: true},
		{name: "altered managed metadata without matching checksum", mutate: func(inventory map[string]any) {
			inventory["assets"].([]any)[3].(map[string]any)["contentHash"] = strings.Repeat("0", 64)
		}},
		{name: "missing published workload", mutate: func(inventory map[string]any) {
			inventory["publish"].(map[string]any)["id"] = "publish_missing"
		}, rehash: true},
		{name: "forged application identity with recomputed checksums", mutate: func(inventory map[string]any) {
			inventory["application"].(map[string]any)["containerId"] = strings.Repeat("f", 64)
		}, rehash: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(valid, &value); err != nil {
				t.Fatal(err)
			}
			preservation := value["execution"].(map[string]any)["preservation"].(map[string]any)
			inventory := preservation["inventory"].(map[string]any)
			test.mutate(inventory)
			if test.rehash {
				document, err := json.Marshal(inventory)
				if err != nil {
					t.Fatal(err)
				}
				var typed V010StateInventory
				if err := json.Unmarshal(document, &typed); err != nil {
					t.Fatal(err)
				}
				hash, err := V010StateInventorySHA256(typed)
				if err != nil {
					t.Fatal(err)
				}
				preservation["beforeSha256"] = hash
				preservation["afterSha256"] = hash
			}
			document, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateV010ReleaseIdentityEvidence(document, v010CandidateBoundEvidencePolicy(t)); err == nil {
				t.Fatal("forged stopped-state inventory evidence was accepted")
			}
		})
	}
}

func TestValidateV010ReleaseIdentityEvidenceRejectsForgedFreshCandidateDenial(t *testing.T) {
	valid, err := os.ReadFile("testdata/v010-release-identity.executed.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "candidate identity", mutate: func(fresh map[string]any) {
			fresh["candidate"].(map[string]any)["image"] = "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("5", 64)
		}},
		{name: "fresh decision", mutate: func(fresh map[string]any) {
			fresh["freshInstallDecision"].(map[string]any)["reasonCode"] = ReasonDeniedNoExplicitRule
		}},
		{name: "denial reason", mutate: func(fresh map[string]any) {
			fresh["denials"].([]any)[0].(map[string]any)["reasonCode"] = ReasonDeniedUnknownRelease
		}},
		{name: "forged matching denial checksums", mutate: func(fresh map[string]any) {
			denial := fresh["denials"].([]any)[0].(map[string]any)
			denial["beforeSha256"] = strings.Repeat("4", 64)
			denial["afterSha256"] = strings.Repeat("4", 64)
		}},
		{name: "mutation proof disabled", mutate: func(fresh map[string]any) { fresh["mutationFree"] = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(valid, &value); err != nil {
				t.Fatal(err)
			}
			fresh := value["execution"].(map[string]any)["freshCandidate"].(map[string]any)
			test.mutate(fresh)
			document, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateV010ReleaseIdentityEvidence(document, v010CandidateBoundEvidencePolicy(t)); err == nil {
				t.Fatal("forged fresh-candidate denial evidence was accepted")
			}
		})
	}
}

func v010CandidateBoundEvidencePolicy(t *testing.T) []byte {
	t.Helper()
	base, err := EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	bound, err := base.BindCandidate(ReleaseIdentity{
		Version: "0.2.0-rc.2", SourceRevision: strings.Repeat("4", 40),
		Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("4", 64), Distribution: "public",
	}, []string{"linux/amd64", "linux/arm64"})
	if err != nil {
		t.Fatal(err)
	}
	document, err := MarshalPolicy(bound)
	if err != nil {
		t.Fatal(err)
	}
	return document
}
