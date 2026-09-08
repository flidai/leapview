package observation

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
)

func testManifest(t *testing.T) Manifest {
	t.Helper()
	files := []File{
		{Path: "orders.csv", SHA256: strings.Repeat("a", 64), StorageKey: "s3://bucket-a/data/orders.csv", Size: 3},
		{Path: "customers.csv", SHA256: strings.Repeat("b", 64), StorageKey: "s3://bucket-a/data/customers.csv", Size: 5},
	}
	managedManifest := manageddata.Manifest{Files: []manageddata.File{
		{Path: files[0].Path, SHA256: files[0].SHA256, Size: files[0].Size},
		{Path: files[1].Path, SHA256: files[1].SHA256, Size: files[1].Size},
	}}
	revision := Revision{
		RevisionID:     "revision-2026-09-08",
		ManifestDigest: managedManifest.RevisionID(),
		Files:          files,
	}
	inventory := Inventory{SchemaVersion: SchemaVersion, Revisions: []Revision{revision}}
	inventoryDigest, err := inventory.Digest()
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		Boundary: Boundary{
			ProtocolVersion: ProtocolVersion, DatabaseIdentity: "control_db", SystemIdentity: "12345",
			Timeline: 7, LSN: "0/16B6C50", RestorePointName: "restore_orders", InventoryDigest: inventoryDigest,
		},
		Inventory: inventory,
		Objects: []Observation{
			{RevisionID: revision.RevisionID, Path: files[0].Path, Object: ProviderObject{
				Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", Bucket: "bucket-a", Key: "data/orders.csv",
				VersionID: "version-orders", SHA256: files[0].SHA256, Size: files[0].Size,
			}},
			{RevisionID: revision.RevisionID, Path: files[1].Path, Object: ProviderObject{
				Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", Bucket: "bucket-a", Key: "data/customers.csv",
				VersionID: "version-customers", SHA256: files[1].SHA256, Size: files[1].Size,
			}},
		},
	}
	return manifest
}

func cloneManifest(manifest Manifest) Manifest {
	clone := manifest
	clone.Inventory.Revisions = make([]Revision, len(manifest.Inventory.Revisions))
	for index, revision := range manifest.Inventory.Revisions {
		clone.Inventory.Revisions[index] = revision
		clone.Inventory.Revisions[index].Files = make([]File, len(revision.Files))
		copy(clone.Inventory.Revisions[index].Files, revision.Files)
	}
	clone.Objects = make([]Observation, len(manifest.Objects))
	copy(clone.Objects, manifest.Objects)
	return clone
}

func reverse[T any](values []T) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func TestManifestCanonicalJSONIsOrderIndependent(t *testing.T) {
	first := testManifest(t)
	second := cloneManifest(first)
	reverse(second.Inventory.Revisions[0].Files)
	reverse(second.Objects)
	firstJSON, err := first.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := second.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("canonical JSON differs:\n%s\n%s", firstJSON, secondJSON)
	}
	firstDigest, err := first.Digest()
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := second.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest || !strings.HasPrefix(firstDigest, "sha256:") {
		t.Fatalf("digests differ or are not canonical: %q %q", firstDigest, secondDigest)
	}
	if !strings.Contains(string(firstJSON), `"schema_version"`) || strings.Contains(string(firstJSON), `"storageKey"`) {
		t.Fatalf("JSON does not use the explicit snake_case contract: %s", firstJSON)
	}
}

func TestManifestStrictParseAndRequiredArrays(t *testing.T) {
	manifest := testManifest(t)
	canonical, err := manifest.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(canonical)
	if err != nil {
		t.Fatalf("Parse(canonical) error: %v", err)
	}
	parsedDigest, err := parsed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if parsedDigest != manifestDigest {
		t.Fatalf("parsed manifest digest = %q, source = %q", parsedDigest, manifestDigest)
	}
	cases := []struct {
		name string
		raw  []byte
	}{
		{name: "unknown field", raw: appendJSONField(canonical, `"unknown":true`)},
		{name: "duplicate field", raw: []byte(`{"schema_version":1,"schema_version":1}`)},
		{name: "missing fields", raw: []byte(`{}`)},
		{name: "oversized", raw: bytes.Repeat([]byte("x"), MaxManifestBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.raw); err == nil {
				t.Fatalf("Parse accepted invalid %s", tc.name)
			}
		})
	}
	zero := testManifest(t)
	zero.Inventory.Revisions[0].Files[0].Size = 0
	zero.Objects[0].Object.Size = 0
	zeroManaged := manageddata.Manifest{Files: []manageddata.File{
		{Path: zero.Inventory.Revisions[0].Files[0].Path, SHA256: zero.Inventory.Revisions[0].Files[0].SHA256, Size: zero.Inventory.Revisions[0].Files[0].Size},
		{Path: zero.Inventory.Revisions[0].Files[1].Path, SHA256: zero.Inventory.Revisions[0].Files[1].SHA256, Size: zero.Inventory.Revisions[0].Files[1].Size},
	}}
	zero.Inventory.Revisions[0].ManifestDigest = zeroManaged.RevisionID()
	zero.Boundary.InventoryDigest, err = zero.Inventory.Digest()
	if err != nil {
		t.Fatal(err)
	}
	zeroJSON, err := zero.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	missingZeroSize := bytes.Replace(zeroJSON, []byte(`,"size":0`), nil, 1)
	if _, err := Parse(missingZeroSize); err == nil {
		t.Fatal("Parse accepted an omitted zero-valued size field")
	}
	caseAlias := bytes.Replace(canonical, []byte(`"schema_version"`), []byte(`"SCHEMA_VERSION"`), 1)
	if _, err := Parse(caseAlias); err == nil {
		t.Fatal("Parse accepted a case-aliased schema field")
	}
	nullScalar := bytes.Replace(canonical, []byte(`"size":3`), []byte(`"size":null`), 1)
	if _, err := Parse(nullScalar); err == nil {
		t.Fatal("Parse accepted a null scalar field")
	}
	unknownNested := bytes.Replace(canonical, []byte(`"version_id":"version-customers"`), []byte(`"version_id":"version-customers","unknown_nested":true`), 1)
	if _, err := Parse(unknownNested); err == nil {
		t.Fatal("Parse accepted an unknown nested field")
	}
	empty := testManifest(t)
	empty.Inventory.Revisions = []Revision{}
	empty.Objects = []Observation{}
	digest, err := empty.Inventory.Digest()
	if err != nil {
		t.Fatal(err)
	}
	empty.Boundary.InventoryDigest = digest
	emptyJSON, err := empty.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(emptyJSON), `"revisions":[]`) || !strings.Contains(string(emptyJSON), `"objects":[]`) {
		t.Fatalf("empty arrays were not canonicalized as arrays: %s", emptyJSON)
	}
	if _, err := Parse(emptyJSON); err != nil {
		t.Fatalf("Parse(explicit empty arrays) error: %v", err)
	}
	empty.Inventory.Revisions = nil
	if err := empty.Validate(); err == nil {
		t.Fatal("Validate accepted omitted inventory revisions")
	}
	boundaryJSON, err := manifest.Boundary.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	boundaryCases := [][]byte{
		[]byte(`{"protocol_version":1,"database_identity":"db","system_identity":"1","lsn":"0/1","restore_point_name":"rp","inventory_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		[]byte(`{"PROTOCOL_VERSION":1,"database_identity":"db","system_identity":"1","timeline":1,"lsn":"0/1","restore_point_name":"rp","inventory_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		[]byte(`{"protocol_version":1,"database_identity":"db","system_identity":"1","timeline":1,"lsn":"0/1","restore_point_name":"rp","inventory_digest":null}`),
	}
	for index, invalidBoundary := range boundaryCases {
		if _, err := ParseBoundary(invalidBoundary); err == nil {
			t.Fatalf("ParseBoundary accepted invalid boundary case %d", index)
		}
	}
	if _, err := ParseBoundary(boundaryJSON); err != nil {
		t.Fatalf("ParseBoundary(canonical) error: %v", err)
	}
}

func appendJSONField(raw []byte, field string) []byte {
	result := append([]byte(nil), raw...)
	result[len(result)-1] = ','
	result = append(result, []byte(field+"}")...)
	return result
}

func TestManifestValidationRejectsBrokenBijectionAndConflicts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{name: "missing observation", mutate: func(m *Manifest) { m.Objects = m.Objects[:1] }, want: "inventory has"},
		{name: "wrong hash", mutate: func(m *Manifest) { m.Objects[0].Object.SHA256 = strings.Repeat("c", 64) }, want: "hash does not match"},
		{name: "wrong storage key", mutate: func(m *Manifest) { m.Objects[0].Object.Key = "other.csv" }, want: "storage key"},
		{name: "duplicate observation", mutate: func(m *Manifest) { m.Objects = append(m.Objects, m.Objects[0]) }, want: "duplicate observation"},
		{name: "unknown inventory member", mutate: func(m *Manifest) { m.Objects[0].Path = "missing.csv" }, want: "no inventory file"},
		{name: "version omitted", mutate: func(m *Manifest) { m.Objects[0].Object.VersionID = "" }, want: "version ID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := testManifest(t)
			tc.mutate(&manifest)
			err := manifest.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate error = %v, want substring %q", err, tc.want)
			}
		})
	}
	conflicting := testManifest(t)
	conflicting.Inventory.Revisions[0].Files[1].StorageKey = conflicting.Inventory.Revisions[0].Files[0].StorageKey
	conflicting.Inventory.Revisions[0].Files[1].SHA256 = strings.Repeat("c", 64)
	conflicting.Inventory.Revisions[0].Files[1].Size = 9
	managedManifest := manageddata.Manifest{Files: []manageddata.File{
		{Path: conflicting.Inventory.Revisions[0].Files[0].Path, SHA256: conflicting.Inventory.Revisions[0].Files[0].SHA256, Size: conflicting.Inventory.Revisions[0].Files[0].Size},
		{Path: conflicting.Inventory.Revisions[0].Files[1].Path, SHA256: conflicting.Inventory.Revisions[0].Files[1].SHA256, Size: conflicting.Inventory.Revisions[0].Files[1].Size},
	}}
	conflicting.Inventory.Revisions[0].ManifestDigest = managedManifest.RevisionID()
	digest, err := conflicting.Inventory.Digest()
	if err != nil {
		t.Fatal(err)
	}
	conflicting.Boundary.InventoryDigest = digest
	conflicting.Objects[1].Object.Key = "data/orders.csv"
	conflicting.Objects[1].Object.VersionID = conflicting.Objects[0].Object.VersionID
	conflicting.Objects[1].Object.SHA256 = strings.Repeat("c", 64)
	conflicting.Objects[1].Object.Size = 9
	if err := conflicting.Validate(); err == nil || !strings.Contains(err.Error(), "conflicting bytes") {
		t.Fatalf("conflicting provider object error = %v", err)
	}
}

func TestBoundaryIdentityAndExactComparison(t *testing.T) {
	manifest := testManifest(t)
	boundary := manifest.Boundary
	if got, want := boundary.RecoveryIdentity(), "postgres:12345:7:0/16B6C50:restore_orders"; got != want {
		t.Fatalf("RecoveryIdentity() = %q, want %q", got, want)
	}
	if got, want := boundary.ClusterIdentity(), "postgres:12345"; got != want {
		t.Fatalf("ClusterIdentity() = %q, want %q", got, want)
	}
	if !boundary.Matches(boundary) {
		t.Fatal("boundary does not match itself")
	}
	changed := boundary
	changed.LSN = "0/16B6C51"
	if boundary.Matches(changed) || boundary.ValidateAgainst(changed) == nil {
		t.Fatal("boundary comparison ignored changed LSN")
	}
	for _, invalid := range []Boundary{
		{ProtocolVersion: ProtocolVersion, DatabaseIdentity: "db", SystemIdentity: "01", Timeline: 1, LSN: "0/1", RestorePointName: "rp", InventoryDigest: strings.Repeat("x", 71)},
		{ProtocolVersion: ProtocolVersion, DatabaseIdentity: "db", SystemIdentity: "1", Timeline: 1, LSN: "0/0001", RestorePointName: "rp", InventoryDigest: strings.Repeat("x", 71)},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatal("Validate accepted invalid boundary")
		}
	}
}

func TestProviderEndpointVersionAndRetention(t *testing.T) {
	manifest := testManifest(t)
	object := manifest.Objects[0].Object
	for _, endpoint := range []string{"http://example.invalid", "https://user:secret@example.invalid", "https://example.invalid/path"} {
		invalid := object
		invalid.Endpoint = endpoint
		if err := invalid.Validate(); err == nil {
			t.Fatalf("ProviderObject accepted endpoint %q", endpoint)
		}
	}
	invalid := object
	invalid.VersionID = "null"
	if err := invalid.Validate(); err == nil {
		t.Fatal("ProviderObject accepted null version ID")
	}
	protection := Protection{Object: object, Mode: "legal_hold", RetainUntil: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)}
	if err := protection.ValidateAt(time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := protection.ValidateAt(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("ValidateAt accepted an expired retention deadline")
	}
}
