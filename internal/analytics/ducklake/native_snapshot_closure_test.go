package ducklake

import (
	"strings"
	"testing"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

func TestCanonicalNativeRelationsSortAndDeduplicate(t *testing.T) {
	got, err := canonicalNativeRelations([]BaseTable{
		{Schema: "z", Table: "last"},
		{Schema: "model", Table: "orders"},
		{Schema: "model", Table: "orders"},
		{Schema: "model", Table: "events"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []BaseTable{{Schema: "model", Table: "events"}, {Schema: "model", Table: "orders"}, {Schema: "z", Table: "last"}}
	if len(got) != len(want) {
		t.Fatalf("relations=%#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("relations=%#v, want %#v", got, want)
		}
	}
}

func TestCanonicalNativeRelationsRejectsCrossNamespaceLeakage(t *testing.T) {
	if _, err := canonicalNativeRelations([]BaseTable{
		{Schema: "_candidate", Table: "orders"},
		{Schema: "model", Table: "orders"},
	}, "_candidate"); err == nil {
		t.Fatal("relation manifest accepted a table from another schema")
	}
}

func TestValidateNativeRelationNamespaceFailsClosed(t *testing.T) {
	for _, namespace := range []string{"", "candidate schema", "Candidate", "candidate;drop", strings.Repeat("a", 64)} {
		if err := validateNativeRelationNamespace(namespace); err == nil {
			t.Fatalf("relation namespace %q unexpectedly accepted", namespace)
		}
	}
	if err := validateNativeRelationNamespace("_candidate_01"); err != nil {
		t.Fatalf("canonical relation namespace rejected: %v", err)
	}
}

func TestCanonicalNativeObjectsCanonicalizesRootsAndRejectsConflicts(t *testing.T) {
	root := "/var/lib/leapview/data"
	got, err := canonicalNativeObjects(root, CatalogFileSet{
		DataFiles:   []string{"part-2.parquet", root + "/part-1.parquet", root + "/part-2.parquet"},
		DeleteFiles: []string{"deletes/part-3.puffin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []NativeSnapshotObject{
		{Kind: DeleteFile, Path: root + "/deletes/part-3.puffin"},
		{Kind: DataFile, Path: root + "/part-1.parquet"},
		{Kind: DataFile, Path: root + "/part-2.parquet"},
	}
	if len(got) != len(want) {
		t.Fatalf("objects=%#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("objects=%#v, want %#v", got, want)
		}
	}
	if _, err := canonicalNativeObjects(root, CatalogFileSet{DataFiles: []string{"same.parquet"}, DeleteFiles: []string{root + "/same.parquet"}}); err == nil {
		t.Fatal("conflicting data/delete reference accepted")
	}
	if _, err := canonicalNativeObjects(root, CatalogFileSet{DataFiles: []string{"../outside.parquet"}}); err == nil {
		t.Fatal("out-of-root reference accepted")
	}
}

func TestCanonicalNativeObjectsSupportsObjectStoreRoots(t *testing.T) {
	root := "s3://Bucket/lake"
	got, err := canonicalNativeObjects(root, CatalogFileSet{
		DataFiles: []string{"s3://bucket/lake/a.parquet", "nested/b.parquet"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []NativeSnapshotObject{
		{Kind: DataFile, Path: "s3://bucket/lake/a.parquet"},
		{Kind: DataFile, Path: "s3://bucket/lake/nested/b.parquet"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("objects=%#v, want %#v", got, want)
		}
	}
	if _, err := canonicalNativeObjects(root, CatalogFileSet{DataFiles: []string{"s3://bucket/other/a.parquet"}}); err == nil {
		t.Fatal("out-of-root object-store reference accepted")
	}
}

func TestNativeSnapshotClosureEvidenceCanonicalDigestsAreStable(t *testing.T) {
	relations := []BaseTable{{Schema: "model", Table: "events"}}
	objects := []NativeSnapshotObject{{Kind: DataFile, Path: "/var/lib/leapview/data/events.parquet"}}
	one, err := newNativeSnapshotClosureEvidence("catalog", 42, "/var/lib/leapview/data", "model", relations, objects)
	if err != nil {
		t.Fatal(err)
	}
	two, err := newNativeSnapshotClosureEvidence("catalog", 42, "/var/lib/leapview/data", "model", relations, objects)
	if err != nil {
		t.Fatal(err)
	}
	if string(one.RelationManifestJSON) != string(two.RelationManifestJSON) || string(one.ClosureJSON) != string(two.ClosureJSON) || string(one.CanonicalJSON) != string(two.CanonicalJSON) {
		t.Fatal("canonical evidence JSON changed between equal inputs")
	}
	if one.RelationManifestDigest != nativeSnapshotDigest(one.RelationManifestJSON) || one.ClosureDigest != nativeSnapshotDigest(one.ClosureJSON) || one.ObjectRootDigest != nativeSnapshotDigest([]byte(one.ObjectRoot)) {
		t.Fatalf("evidence digest does not match canonical bytes: %#v", one)
	}
	for name, value := range map[string]string{"relation": one.RelationManifestDigest, "closure": one.ClosureDigest, "root": one.ObjectRootDigest} {
		if err := platformdigest.ValidateSHA256Identity(value); err != nil {
			t.Fatalf("%s digest %q: %v", name, value, err)
		}
	}
	if !strings.Contains(string(one.CanonicalJSON), `"schema_version":2`) || !strings.Contains(string(one.CanonicalJSON), `"relation_namespace":"model"`) {
		t.Fatalf("canonical envelope omitted schema version: %s", one.CanonicalJSON)
	}
}

func TestNativeSnapshotClosureEvidencePreservesEmptyArrayCanonicalDocuments(t *testing.T) {
	evidence, err := newNativeSnapshotClosureEvidence("catalog-empty", 42, "/var/lib/leapview/data", "_empty_candidate", []BaseTable{}, []NativeSnapshotObject{})
	if err != nil {
		t.Fatal(err)
	}
	if string(evidence.RelationManifestJSON) != `{"relation_namespace":"_empty_candidate","relations":[]}` {
		t.Fatalf("relation manifest = %s, want empty array", evidence.RelationManifestJSON)
	}
	if string(evidence.ClosureJSON) != `{"objects":[]}` {
		t.Fatalf("closure manifest = %s, want empty array", evidence.ClosureJSON)
	}
	canonical := string(evidence.CanonicalJSON)
	if !strings.Contains(canonical, `"relations":[]`) || !strings.Contains(canonical, `"objects":[]`) {
		t.Fatalf("canonical envelope does not preserve empty arrays: %s", canonical)
	}
	if strings.Contains(canonical, `"relations":null`) || strings.Contains(canonical, `"objects":null`) {
		t.Fatalf("canonical envelope contains null arrays: %s", canonical)
	}
	if err := VerifyNativeSnapshotClosureEvidence(evidence); err == nil || !strings.Contains(err.Error(), "relation manifest is empty") {
		t.Fatalf("empty native closure verification error = %v", err)
	}
}

func TestVerifyNativeSnapshotClosureEvidenceRejectsSelfConsistentNonCanonicalValues(t *testing.T) {
	root := "/var/lib/leapview/data"
	relation := BaseTable{Schema: "_candidate", Table: "orders"}
	for name, test := range map[string]struct {
		relations []BaseTable
		objects   []NativeSnapshotObject
	}{
		"duplicate relation": {relations: []BaseTable{relation, relation}},
		"out of root object": {relations: []BaseTable{relation}, objects: []NativeSnapshotObject{{Kind: DataFile, Path: "/var/lib/other/orders.parquet"}}},
	} {
		t.Run(name, func(t *testing.T) {
			evidence, err := newNativeSnapshotClosureEvidence("catalog", 42, root, "_candidate", test.relations, test.objects)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyNativeSnapshotClosureEvidence(evidence); err == nil {
				t.Fatal("self-consistent non-canonical evidence was accepted")
			}
		})
	}
}
