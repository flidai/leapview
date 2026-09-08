package project_test

import (
	"encoding/json"
	"strings"
	"testing"

	project "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestResourceUIDBindingRejectsUnsealedOrDriftingContractEvidence(t *testing.T) {
	inventory, err := project.NewResourceUIDInventory(inventoryBundle(t, true))
	if err != nil { t.Fatal(err) }
	encoded, _ := inventory.JSON()
	var entries []struct {
		ID string `json:"authored_id"`
		Profile string `json:"contract_profile"`
		Version string `json:"contract_version"`
		Canonical string `json:"canonical_contract"`
		Digest string `json:"contract_digest"`
	}
	if err := json.Unmarshal(encoded, &entries); err != nil { t.Fatal(err) }
	entry := entries[1]
	instance := "lvinst_" + strings.Repeat("a", 32)
	valid := project.ResourceUIDBinding{
		ResourceUID: "0198f2c0-7c7a-7f00-8a11-000000001001", InstanceID: instance, TargetID: instance,
		ProjectID: "project:test", Environment: "prod", GenerationID: "0198f2c0-7c7a-7f00-8a11-000000001002",
		AuthoredID: entry.ID, Kind: projectgraph.KindSource, ContractStatus: "canonical",
		ContractProfile: entry.Profile, ContractVersion: entry.Version, ContractDigest: entry.Digest, ContractBytes: []byte(entry.Canonical),
	}
	if err := valid.Validate(); err != nil { t.Fatalf("valid evidence: %v", err) }
	for name, change := range map[string]func(*project.ResourceUIDBinding){
		"missing status": func(b *project.ResourceUIDBinding) { b.ContractStatus = "" },
		"missing version": func(b *project.ResourceUIDBinding) { b.ContractVersion = "" },
		"different version": func(b *project.ResourceUIDBinding) { b.ContractVersion = "2.0.0" },
		"different resource": func(b *project.ResourceUIDBinding) { b.AuthoredID = "source:other" },
		"different kind": func(b *project.ResourceUIDBinding) { b.Kind = projectgraph.KindModel },
		"hash-shaped fake": func(b *project.ResourceUIDBinding) { b.ContractBytes = []byte(`{}`) },
		"different hash": func(b *project.ResourceUIDBinding) { b.ContractDigest = "sha256:" + strings.Repeat("0", 64) },
		"invalid scope": func(b *project.ResourceUIDBinding) { b.TargetID = "lvinst_" + strings.Repeat("b", 32) },
		"invented unversioned evidence": func(b *project.ResourceUIDBinding) { b.ContractStatus = "unversioned" },
	} {
		t.Run(name, func(t *testing.T) { changed := valid; change(&changed); if changed.Validate() == nil { t.Fatal("accepted invalid binding") } })
	}
	unversioned := valid
	unversioned.ContractStatus = "unversioned"
	unversioned.ContractProfile, unversioned.ContractVersion, unversioned.ContractDigest, unversioned.ContractBytes = "", "", "", nil
	if err := unversioned.Validate(); err != nil { t.Fatalf("unversioned: %v", err) }
	unversioned.ContractStatus = ""
	if unversioned.Validate() == nil { t.Fatal("accepted omitted status as an unversioned fallback") }
}

func TestResourceUIDRejectsNonCanonicalIdentity(t *testing.T) {
	for _, value := range []string{"", "00000000-0000-0000-0000-000000000000", "0198F2C0-7C7A-7F00-8A11-000000001001", "source:orders"} {
		if _, err := project.ParseResourceUID(value); err == nil { t.Fatalf("accepted %q", value) }
	}
}
