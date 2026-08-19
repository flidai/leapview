package extensionsupplyloader

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/app/testing/extensionfixture"
	"github.com/flidai/leapview/internal/deployment/extensionsupply"
	"github.com/flidai/leapview/internal/extension"
)

func TestVerifyDuckDBArtifactAtPathLoadsExactStagedArtifact(t *testing.T) {
	fixture := extensionfixture.New(t, "httpfs")
	admitted, err := fixture.Admission.AdmitExtension(context.Background(), "httpfs")
	if err != nil {
		t.Fatal(err)
	}
	artifact := extensionsupply.Artifact{Identity: extension.Identity{
		DuckDBVersion: admitted.DuckDBVersion, ExtensionVersion: admitted.ExtensionVersion,
		GOOS: admitted.GOOS, GOARCH: admitted.GOARCH, Platform: admitted.Platform,
		Name: admitted.Name, Digest: admitted.Digest, SupportProfile: admitted.SupportProfile,
	}, Provenance: admitted.Provenance, Signature: admitted.Signature}
	if err := verifyDuckDBArtifactAtPath(context.Background(), artifact, admitted.Path); err != nil {
		t.Fatalf("exact-path verifier rejected reviewed fixture: %v", err)
	}
}
