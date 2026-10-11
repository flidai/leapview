package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/analytics/ducklake/metadata"
	"github.com/flidai/leapview/internal/app/adminpostgres"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/managedrecovery"
	appobjectstore "github.com/flidai/leapview/internal/app/objectstore"
	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/objectstore"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// This private export transports a real publication into disposable installed
// module guests. It grants no protected artifact, fence or qualification proof.
// No private export bytes are ever placed in the Nix store.
type managedInstalledExport struct {
	SchemaVersion               int                                    `json:"schemaVersion"`
	SourceRevision              string                                 `json:"sourceRevision"`
	ProducerSourceDirty         bool                                   `json:"producerSourceDirty"`
	MetadataSchema              string                                 `json:"metadataSchema"`
	Scope                       string                                 `json:"scope"`
	ProducerSHA256              string                                 `json:"producerSHA256"`
	Set                         recoveryset.RecoverySet                `json:"set"`
	Closure                     metadata.NativeSnapshotClosureEvidence `json:"closure"`
	Roots                       []managedrecovery.ResticConfig         `json:"roots"`
	Credentials                 managedrecovery.ManagedCredentials     `json:"credentials"`
	Roles                       managedrecovery.RuntimeRoles           `json:"roles"`
	Config                      config.Config                          `json:"config"`
	SourceAdminURL              string                                 `json:"sourceAdminURL"`
	ActivationQualified         bool                                   `json:"activationQualified"`
	ReleaseAdmissionQualified   bool                                   `json:"releaseAdmissionQualified"`
	FullManagedProfileQualified bool                                   `json:"fullManagedProfileQualified"`
}

func TestManagedRecoveryInstalledPublicationExport(t *testing.T) {
	destination := os.Getenv("LEAPVIEW_TEST_MANAGED_EXPORT_DIR")
	if destination == "" {
		t.Skip("explicit private disposable installed-journey export required")
	}
	require.NoError(t, managedInstalledExportDestination(destination))
	pgbin, restic := os.Getenv("LEAPVIEW_TEST_MANAGED_POSTGRES_BIN"), os.Getenv("LEAPVIEW_TEST_MANAGED_RESTIC")
	require.NotEmpty(t, pgbin)
	require.NotEmpty(t, restic)
	f, _ := runFirstSourceProductionPublicationJourneyBeforeRestart(t, false, func(f *sourceCredentialHTTPJourney) { managedInstalledAwaitPublication(t, f) })
	require.NoError(t, f.target.Shutdown(context.Background()))
	f.target = nil
	native := managedJourneyReadback(t, f)
	_, err := native.Verify(t.Context())
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	producer, err := os.ReadFile(executable)
	require.NoError(t, err)
	value := managedInstalledExport{SchemaVersion: 1, Scope: "disposable-installed-coordinator-component", ProducerSHA256: managedJourneyDigest(producer), SourceRevision: buildinfo.Current().Revision, ProducerSourceDirty: buildinfo.Current().Dirty, Set: native.Set, Closure: managedJourneyNativeClosure(t, f, native.Set.Serving), Credentials: *native.Credentials, Roles: native.Roles, Config: f.config, SourceAdminURL: f.control.AdminURL(), MetadataSchema: native.MetadataSchema}
	value.Credentials.ControlURL, value.Credentials.DuckLakeURL = native.ControlURL, native.DuckLakeURL
	run := func(program string, args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(t.Context(), program, args...)
		command.Env = []string{"PATH=/nonexistent", "LANG=C", "TZ=UTC"}
		output, err := command.Output()
		require.NoError(t, err, "disposable export provider failed; arguments and stderr withheld")
		return output
	}
	run(filepath.Join(pgbin, "pg_basebackup"), "--dbname="+f.harness.PhysicalBackupURL(t), "--pgdata="+filepath.Join(destination, "pgdata"), "--wal-method=stream", "--checkpoint=fast")
	password := filepath.Join(destination, "restic-password")
	require.NoError(t, os.WriteFile(password, []byte("disposable installed-publication component password"), 0600))
	repository := filepath.Join(destination, "repository")
	resticRun := func(args ...string) []byte {
		return run(restic, append([]string{"--no-cache", "--repo", repository, "--password-file", password}, args...)...)
	}
	resticRun("init")
	store, _, err := appobjectstore.New(t.Context(), f.config, f.instance, f.config.Environment)
	require.NoError(t, err)
	value.Set.ObjectRoots = nil
	for _, root := range native.Set.ObjectRoots {
		storage := ""
		var objectMetadata *objectstore.ObjectMetadata
		if root.Kind == recoveryset.ObjectRootServingArtifact {
			storage = filepath.Join(f.config.ArtifactDir(), "object-store")
			object, err := store.Open(t.Context(), root.URI)
			require.NoError(t, err)
			_, err = io.Copy(io.Discard, object.Body)
			require.NoError(t, err)
			require.NoError(t, object.Body.Close())
			i := object.Info
			objectMetadata = &objectstore.ObjectMetadata{StorageSecurityDomain: i.StorageSecurityDomain, Digest: i.Digest, SizeBytes: i.SizeBytes, ContentType: i.ContentType, MetadataDigest: i.MetadataDigest}
		}
		source, err := providerrestore.ManagedLocalRootPath(root, storage)
		require.NoError(t, err)
		manifestRoot := source
		if objectMetadata != nil {
			manifestRoot = filepath.Dir(source)
		}
		manifest, err := managedrecovery.CaptureFiles(manifestRoot)
		require.NoError(t, err)
		if objectMetadata != nil {
			selected := []managedrecovery.FileContent{}
			for _, file := range manifest.Files {
				if file.Path == filepath.Base(source) {
					selected = append(selected, file)
				}
			}
			require.Len(t, selected, 1)
			manifest.Files = selected
		} else {
			require.NoError(t, managedrecovery.VerifyManagedClosure(value.Set.Serving, value.Closure, manifest))
		}
		manifestDigest, err := manifest.Digest()
		require.NoError(t, err)
		var snapshot string
		for _, line := range bytes.Split(resticRun("backup", "--json", source), []byte("\n")) {
			var result struct {
				Type string `json:"message_type"`
				ID   string `json:"snapshot_id"`
			}
			if json.Unmarshal(line, &result) == nil && result.Type == "summary" {
				snapshot = result.ID
			}
		}
		require.Len(t, snapshot, 64)
		root.VersionID, root.ProviderRecoveryFrontier = snapshot, "restic:"+snapshot
		value.Set.ObjectRoots = append(value.Set.ObjectRoots, root)
		value.Roots = append(value.Roots, managedrecovery.ResticConfig{TargetID: f.instance, RecoverySetID: value.Set.ID, Root: root, StorageRoot: storage, Restic: restic, Repository: repository, PasswordFile: password, Destination: source, Manifest: manifest, ManifestDigest: manifestDigest, ArtifactMetadata: objectMetadata})
	}
	keyring, err := os.ReadFile(f.config.CredentialKeyringFile)
	require.NoError(t, err)
	require.Equal(t, value.Credentials.KeyringDigest, managedJourneyDigest(keyring))
	require.NoError(t, os.WriteFile(filepath.Join(destination, "keyring"), keyring, 0600))
	require.NoError(t, value.Set.Validate())
	payload, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(destination, "export.json"), payload, 0600))
	// Bind exact immutable export bytes independently of the controller binary.
	require.NoError(t, os.WriteFile(filepath.Join(destination, "export.sha256"), []byte(managedJourneyDigest(payload)), 0600))
	manifest, err := managedrecovery.CaptureFiles(destination)
	require.NoError(t, err)
	manifestBytes, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(destination, "bundle-manifest.json"), manifestBytes, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(destination, "bundle.sha256"), []byte(managedJourneyDigest(manifestBytes)), 0600))
}

func managedInstalledAwaitPublication(t *testing.T, f *sourceCredentialHTTPJourney) {
	t.Helper()
	connection, err := pgx.Connect(t.Context(), f.control.AdminURL())
	require.NoError(t, err)
	defer connection.Close(context.Background())
	require.Eventually(t, func() bool {
		var complete bool
		err := connection.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM jobs.job_history WHERE kind='delivery.approval.activate' AND status='succeeded')`).Scan(&complete)
		return err == nil && complete
	}, time.Minute, 100*time.Millisecond, "actual publication job must durably acknowledge before export")
}

func managedInstalledExportDestination(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return os.ErrInvalid
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return os.ErrPermission
	}
	// A second run cannot overwrite an existing export or follow a final link.
	return os.Mkdir(path, 0700)
}

// The guest invokes the same fixture executable after a real installed source
// module has captured its backup/WAL frontier. Preparation uses production
// maintenance and canonical retention authority, never direct inserted rows.
func TestManagedRecoveryInstalledSourcePreparation(t *testing.T) {
	path := os.Getenv("LEAPVIEW_TEST_MANAGED_PREPARE_FILE")
	if path == "" {
		t.Skip("explicit disposable installed source preparation required")
	}
	var input struct {
		Config      config.Config              `json:"config"`
		Set         recoveryset.RecoverySet    `json:"set"`
		Frontier    managedrecovery.PGFrontier `json:"frontier"`
		ExpiresAt   time.Time                  `json:"expiresAt"`
		ReceiptFile string                     `json:"receiptFile"`
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&input))
	require.Equal(t, io.EOF, decoder.Decode(&struct{}{}))
	require.True(t, strings.HasPrefix(input.ReceiptFile, filepath.Dir(path)+string(filepath.Separator)))
	identity, err := input.Frontier.RecoveryIdentity()
	require.NoError(t, err)
	for index := range input.Set.ClusterPoints {
		require.Equal(t, "postgres-system-id:"+input.Frontier.SystemID, input.Set.ClusterPoints[index].ClusterIdentity)
		input.Set.ClusterPoints[index].RecoveryIdentity = identity
	}
	operations := adminpostgres.New(adminpostgres.Dependencies{LoadConfig: func() (config.Config, error) { return input.Config, nil }})
	prepared, err := operations.PrepareRecovery(t.Context(), admincli.RecoveryPrepareRequest{Set: input.Set, ExpiresAt: input.ExpiresAt})
	require.NoError(t, err, "real installed source preparation failed")
	data, err = json.Marshal(prepared)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(input.ReceiptFile, data, 0600))
}

func TestManagedRecoveryInstalledExportDestination(t *testing.T) {
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0700))
	path := filepath.Join(parent, "export")
	require.NoError(t, managedInstalledExportDestination(path))
	require.ErrorIs(t, managedInstalledExportDestination(path), os.ErrExist)
	require.Error(t, managedInstalledExportDestination(parent+"/../outside"))
	link := filepath.Join(parent, "link")
	require.NoError(t, os.Symlink(path, link))
	require.ErrorIs(t, managedInstalledExportDestination(link), os.ErrExist)
	public := filepath.Join(parent, "public")
	require.NoError(t, os.Mkdir(public, 0755))
	require.ErrorIs(t, managedInstalledExportDestination(filepath.Join(public, "export")), os.ErrPermission)
}
