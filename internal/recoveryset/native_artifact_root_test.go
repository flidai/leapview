package recoveryset

import (
	"strings"
	"testing"
)

func TestNativeServingArtifactRootPreservesExactDigestLocator(t *testing.T) {
	root := ObjectRoot{Kind: ObjectRootServingArtifact, URI: "serving-artifacts/" + strings.Repeat("d", 64) + ".tar.gz", Digest: testDigest('d'), VersionID: "snapshot"}
	if err := root.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ObjectRoot){
		func(r *ObjectRoot) { r.Kind = ObjectRootDuckLake },
		func(r *ObjectRoot) { r.Digest = testDigest('e') },
		func(r *ObjectRoot) { r.URI = "serving-artifacts/../" + strings.Repeat("d", 64) + ".tar.gz" },
		func(r *ObjectRoot) { r.URI += "/extra" },
		func(r *ObjectRoot) { r.URI = "serving-artifacts/latest.tar.gz" },
	} {
		invalid := root
		mutate(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid native artifact root accepted: %+v", invalid)
		}
	}
	set := testSet(t)
	set.Serving.ArtifactRoot = root.URI
	set.ObjectRoots[1] = root
	if err := set.Validate(); err != nil {
		t.Fatal(err)
	}
}
