package managedrecovery

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/stretchr/testify/require"
)

func TestManagedInputReconstructsAndVerifiesNativeClosure(t *testing.T) {
	seal, closure, manifest := managedClosureFixture(t)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	input := ManagedInput{InstanceHome: root, SchemaVersion: 1, Profile: providerrestore.ManagedLocalProfile, RecoverySetID: "set", OccurrenceID: "occurrence", ValidationAttemptID: "validation", Validator: "operator", Publisher: "publisher", Closure: closure}
	path := filepath.Join(root, "restore.json")
	value, err := json.Marshal(input)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, value, 0600))
	retained, err := ReadManagedInput(path)
	require.NoError(t, err)
	require.Equal(t, closure.RelationManifestJSON, retained.Closure.RelationManifestJSON)
	require.Equal(t, closure.ClosureJSON, retained.Closure.ClosureJSON)
	require.Equal(t, closure.CanonicalJSON, retained.Closure.CanonicalJSON)
	require.NoError(t, VerifyManagedClosure(seal, retained.Closure, manifest))
	for name, mutate := range map[string]func(*ManagedInput){
		"empty closure":       func(i *ManagedInput) { i.Closure = ManagedInput{}.Closure },
		"digest":              func(i *ManagedInput) { i.Closure.ClosureDigest = "sha256:" + strings.Repeat("f", 64) },
		"root digest":         func(i *ManagedInput) { i.Closure.ObjectRootDigest = "sha256:" + strings.Repeat("f", 64) },
		"foreign namespace":   func(i *ManagedInput) { i.Closure.RelationNamespace = "foreign" },
		"duplicate objects":   func(i *ManagedInput) { i.Closure.Objects = append(i.Closure.Objects, i.Closure.Objects[0]) },
		"escaping object":     func(i *ManagedInput) { i.Closure.Objects[0].Path = "/outside/part.parquet" },
		"invalid object kind": func(i *ManagedInput) { i.Closure.Objects[0].Kind = "unverified" },
		"oversized field":     func(i *ManagedInput) { i.Closure.CatalogID = strings.Repeat("a", 4097) },
	} {
		t.Run(name, func(t *testing.T) {
			altered := input
			altered.Closure.Objects = slices.Clone(altered.Closure.Objects)
			mutate(&altered)
			encoded, err := json.Marshal(altered)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, encoded, 0600))
			_, err = ReadManagedInput(path)
			require.Error(t, err)
		})
	}
	for name, raw := range map[string][]byte{
		"duplicate closure key":      bytes.Replace(value, []byte(`"catalog_id":`), []byte(`"catalog_id":"foreign","catalog_id":`), 1),
		"unknown closure field":      bytes.Replace(value, []byte(`"closure":{`), []byte(`"closure":{"unknown":true,`), 1),
		"fabricated canonical bytes": bytes.Replace(value, []byte(`"closure":{`), []byte(`"closure":{"CanonicalJSON":"invented",`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(path, raw, 0600))
			_, err := ReadManagedInput(path)
			require.Error(t, err)
		})
	}
}
