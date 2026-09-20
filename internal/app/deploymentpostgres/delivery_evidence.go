package deploymentpostgres

import (
	"fmt"
	"strings"

	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
)

func sameSnapshotSeal(got deploymentnative.SnapshotSeal, want SnapshotSealEvidence) bool {
	return got.SealID == want.SealID && got.AttemptID == want.AttemptID && got.CandidateID == want.CandidateID &&
		got.PhysicalPoolID == want.PhysicalPoolID && got.TenantDomain == want.TenantDomain && got.Region == want.Region && got.EncryptionDomain == want.EncryptionDomain &&
		got.ObjectNamespace == want.ObjectNamespace && got.CatalogDatabase == want.CatalogDatabase && got.CatalogID == want.CatalogID && got.CatalogUUID == want.CatalogUUID &&
		got.CatalogVersion == want.CatalogVersion && got.DuckLakeSnapshotID == want.DuckLakeSnapshotID && got.RelationNamespace == want.RelationNamespace &&
		got.ObjectRoot == want.ObjectRoot && got.ObjectRootDigest == want.ObjectRootDigest && got.ArtifactRoot == want.ArtifactRoot && got.ArtifactRootDigest == want.ArtifactRootDigest &&
		got.RelationManifestDigest == want.RelationManifestDigest && got.ClosureDigest == want.ClosureDigest && got.CompiledGraphDigest == want.CompiledGraphDigest &&
		got.CompiledConfigDigest == want.CompiledConfigDigest && got.SecurityDomainFingerprint == want.SecurityDomainFingerprint &&
		got.AuthorizationPolicyRevision == want.AuthorizationPolicyRevision && got.AuthorizationPolicyDigest == want.AuthorizationPolicyDigest && got.RequestDigest == want.RequestDigest &&
		got.PlanDigest == want.PlanDigest && got.CompatibilityDigest == want.CompatibilityDigest && got.ServingArtifactID == want.ServingArtifactID &&
		got.ServingArtifactDigest == want.ServingArtifactDigest && got.DuckDBVersion == want.DuckDBVersion && got.RuntimeVersion == want.RuntimeVersion &&
		got.DuckLakeExtensionVersion == want.DuckLakeExtensionVersion && got.DuckLakeSpecVersion == want.DuckLakeSpecVersion && got.CatalogSchemaVersion == want.CatalogSchemaVersion &&
		!got.QualifiedAt.IsZero() && sameJSON(got.QualificationEvidence, want.QualificationEvidence) && sameOptionalJSON(got.ResolvedInputs, want.ResolvedInputs) && got.ResolvedInputsDigest == want.ResolvedInputsDigest
}

func sameOptionalJSON(left, right []byte) bool {
	if len(left) == 0 || strings.TrimSpace(string(left)) == "{}" {
		left = []byte(`{}`)
	}
	if len(right) == 0 || strings.TrimSpace(string(right)) == "{}" {
		right = []byte(`{}`)
	}
	return sameJSON(left, right)
}

func validateResolvedInputEvidence(seal SnapshotSealEvidence) error {
	if len(seal.ResolvedInputs) > 0 && strings.TrimSpace(string(seal.ResolvedInputs)) != "{}" {
		if err := validateRequiredObject(seal.ResolvedInputs, "resolved input evidence"); err != nil {
			return err
		}
		if err := validateDigest(seal.ResolvedInputsDigest, "resolved input evidence digest"); err != nil {
			return err
		}
	} else if seal.ResolvedInputsDigest != "" {
		return fmt.Errorf("%w: resolved input evidence digest has no record", deploymentnative.ErrInvalid)
	}
	return nil
}
