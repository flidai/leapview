package providerrestore

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/recoveryset"
)

func managedLocalHandoffFixture(t *testing.T) (recoveryset.RecoverySet, ReplacementHandoff) {
	t.Helper()
	set := providerRestoreSet(t)
	set.ObjectRoots = []recoveryset.ObjectRoot{
		{Kind: recoveryset.ObjectRootDuckLake, URI: "/var/lib/leapview/analytics", VersionID: strings.Repeat("a", 64), Digest: set.Serving.ObjectRootDigest, ProviderRecoveryFrontier: "restic:" + strings.Repeat("a", 64)},
		{Kind: recoveryset.ObjectRootServingArtifact, URI: "serving-artifacts/" + strings.TrimPrefix(set.Serving.ArtifactRootDigest, "sha256:") + ".tar.gz", VersionID: strings.Repeat("b", 64), Digest: set.Serving.ArtifactRootDigest, ProviderRecoveryFrontier: "restic:" + strings.Repeat("b", 64)},
	}
	set.Serving.ObjectRoot = set.ObjectRoots[0].URI
	set.Serving.ArtifactRoot = set.ObjectRoots[1].URI
	handoff := validReplacementHandoff(set)
	handoff.SchemaVersion = ManagedLocalHandoffSchemaVersion
	handoff.Kind = ManagedLocalHandoffKind
	handoff.Providers = slices.Clone(handoff.Providers[:2])
	handoff.Secrets.Keys = []string{"postgres.control.url", "postgres.ducklake.url", "postgres.root-ca", "deployment.keyring"}
	handoff.ManagedLocal = &ManagedLocalHandoff{Profile: ManagedLocalProfile, OccurrenceID: "occurrence-selected", Roots: []ManagedLocalRoot{}}
	for _, root := range set.ObjectRoots {
		entry := ManagedLocalRoot{Root: root, Destination: root.URI, ContentManifestDigest: "sha256:" + strings.Repeat("c", 64)}
		if root.Kind == recoveryset.ObjectRootServingArtifact {
			entry.StorageRoot = "/var/lib/leapview/artifacts"
			entry.Destination = entry.StorageRoot + "/" + root.URI
		}
		handoff.ManagedLocal.Roots = append(handoff.ManagedLocal.Roots, entry)
	}
	return set, handoff
}

func TestManagedLocalHandoffBindsAllAuthoritativeRootsAndTLSReferences(t *testing.T) {
	set, handoff := managedLocalHandoffFixture(t)
	if err := handoff.Validate(set, handoff.Artifact.Image); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*ReplacementHandoff){
		"missing root":           func(h *ReplacementHandoff) { h.ManagedLocal.Roots = h.ManagedLocal.Roots[:1] },
		"duplicate root":         func(h *ReplacementHandoff) { h.ManagedLocal.Roots[1] = h.ManagedLocal.Roots[0] },
		"foreign snapshot":       func(h *ReplacementHandoff) { h.ManagedLocal.Roots[0].Root.VersionID = strings.Repeat("d", 64) },
		"different path":         func(h *ReplacementHandoff) { h.ManagedLocal.Roots[0].Destination = "/empty/replacement" },
		"remote root":            func(h *ReplacementHandoff) { h.ManagedLocal.Roots[0].Root.URI = "s3://bucket/root" },
		"missing content proof":  func(h *ReplacementHandoff) { h.ManagedLocal.Roots[0].ContentManifestDigest = "" },
		"missing storage root":   func(h *ReplacementHandoff) { h.ManagedLocal.Roots[1].StorageRoot = "" },
		"different storage root": func(h *ReplacementHandoff) { h.ManagedLocal.Roots[1].StorageRoot = "/other" },
		"relative storage root":  func(h *ReplacementHandoff) { h.ManagedLocal.Roots[1].StorageRoot = "../escape" },
		"absolute root remapped": func(h *ReplacementHandoff) { h.ManagedLocal.Roots[0].StorageRoot = "/other" },
		"plaintext PG":           func(h *ReplacementHandoff) { h.Providers[0].Endpoint = "postgres://db.example.com?sslmode=disable" },
		"missing keyring":        func(h *ReplacementHandoff) { h.Secrets.Keys = h.Secrets.Keys[:3] },
		"wrong profile":          func(h *ReplacementHandoff) { h.ManagedLocal.Profile = "remote" },
		"remote schema":          func(h *ReplacementHandoff) { h.SchemaVersion = HandoffSchemaVersion; h.Kind = HandoffKind },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			_, test := managedLocalHandoffFixture(t)
			mutate(&test)
			if err := test.Validate(set, test.Artifact.Image); err == nil {
				t.Fatal("mismatched managed-local handoff accepted")
			}
		})
	}
}

func TestManagedLocalHandoffRejectsIncompleteProviderResults(t *testing.T) {
	_, handoff := managedLocalHandoffFixture(t)
	databases := []DatabaseResult{{DatabaseRole: recoveryset.DatabaseControl, DatabaseIdentity: handoff.Providers[0].Database}, {DatabaseRole: recoveryset.DatabaseDuckLake, DatabaseIdentity: handoff.Providers[1].Database}}
	objects := []ObjectResult{}
	for _, entry := range handoff.ManagedLocal.Roots {
		objects = append(objects, ObjectResult{Kind: entry.Root.Kind, URI: entry.Root.URI, RequiredVersionID: entry.Root.VersionID, ObservedVersionID: entry.Root.VersionID, Digest: entry.Root.Digest})
	}
	if err := validateHandoffResults(handoff, databases, objects); err != nil {
		t.Fatal(err)
	}
	if err := validateHandoffResults(handoff, databases, objects[:1]); err == nil {
		t.Fatal("missing recovered local root accepted")
	}
	objects[1] = objects[0]
	if err := validateHandoffResults(handoff, databases, objects); err == nil {
		t.Fatal("duplicate recovered local root accepted")
	}
}

func TestRemoteHandoffEncodingRetainsLegacyShape(t *testing.T) {
	handoff := validReplacementHandoff(providerRestoreSet(t))
	encoded, err := json.Marshal(handoff)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "managedLocal") {
		t.Fatal("legacy remote receipt gained a serialized local profile")
	}
	// Legacy remote profile refuses the new payload even if its old endpoints
	// would otherwise validate. Generic consumers cannot adopt a mixed profile.
	handoff.ManagedLocal = &ManagedLocalHandoff{Profile: ManagedLocalProfile}
	if err := handoff.Validate(providerRestoreSet(t), handoff.Artifact.Image); err == nil {
		t.Fatal("mixed remote/local receipt accepted")
	}
}
