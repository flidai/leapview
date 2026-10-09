package metadata

// These functions expose the same pure value checks to the native observer.
func NewNativeSnapshotClosureEvidence(catalogID string, snapshotID int64, objectRoot, relationNamespace string, relations []BaseTable, objects []NativeSnapshotObject) (NativeSnapshotClosureEvidence, error) {
	return newNativeSnapshotClosureEvidence(catalogID, snapshotID, objectRoot, relationNamespace, relations, objects)
}
func CanonicalNativeRelations(input []BaseTable, relationNamespaces ...string) ([]BaseTable, error) {
	return canonicalNativeRelations(input, relationNamespaces...)
}
func CanonicalNativeObjects(root string, files CatalogFileSet) ([]NativeSnapshotObject, error) {
	return canonicalNativeObjects(root, files)
}
func ValidateNativeIdentityField(name, value string) error {
	return validateNativeIdentityField(name, value)
}
func ValidateNativeRelationNamespace(value string) error {
	return validateNativeRelationNamespace(value)
}
