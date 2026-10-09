package metadata

import "fmt"

// NativeSnapshotClosureEvidenceFromValues reconstructs the canonical byte
// documents omitted by the value-only JSON shape. It verifies the submitted
// ordering, paths and digests before returning private canonical copies; it
// never replaces a mismatched supplied digest with a newly computed one.
func NativeSnapshotClosureEvidenceFromValues(values NativeSnapshotClosureEvidence) (NativeSnapshotClosureEvidence, error) {
	expected, err := newNativeSnapshotClosureEvidence(values.CatalogID, values.SnapshotID, values.ObjectRoot, values.RelationNamespace, values.Relations, values.Objects)
	if err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	if values.ObjectRoot != expected.ObjectRoot {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("DuckLake native object root is not canonical")
	}
	values.RelationManifestJSON = expected.RelationManifestJSON
	values.ClosureJSON = expected.ClosureJSON
	values.CanonicalJSON = expected.CanonicalJSON
	if err := VerifyNativeSnapshotClosureEvidence(values); err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	return expected, nil
}
