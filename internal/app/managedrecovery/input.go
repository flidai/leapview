package managedrecovery

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/flidai/leapview/internal/analytics/ducklake/metadata"
	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/pkg/strictjson"
)

type ManagedPostgresInput struct {
	Provider                string     `json:"provider,omitempty"`
	Frontier                PGFrontier `json:"frontier"`
	Postgres                string     `json:"postgres"`
	PGControlData           string     `json:"pgControlData"`
	PGBackRest              string     `json:"pgBackRest"`
	Bubblewrap              string     `json:"bubblewrap"`
	ConfigFile              string     `json:"configFile"`
	ConfigDigest            string     `json:"configDigest"`
	Destination             string     `json:"destination"`
	MetadataSchema          string     `json:"metadataSchema"`
	ServerCertificateFile   string     `json:"serverCertificateFile"`
	ServerCertificateDigest string     `json:"serverCertificateDigest"`
	ServerKeyFile           string     `json:"serverKeyFile"`
	ServerKeyDigest         string     `json:"serverKeyDigest"`
}

func (input ManagedPostgresInput) validateProvider() error {
	if input.Provider == "" {
		return nil
	}
	if input.Provider != "module-owned" || input.Postgres != "" || input.PGControlData != "" || input.PGBackRest != "" || input.Bubblewrap != "" || input.ConfigFile != "" || input.ConfigDigest != "" || input.ServerCertificateFile != "" || input.ServerCertificateDigest != "" || input.ServerKeyFile != "" || input.ServerKeyDigest != "" {
		return errors.New("module-owned provider forbids caller tools, configuration and service TLS keys")
	}
	return nil
}

// ManagedInput is a private operator document. It names retained secrets by
// explicit file rather than including them in process arguments or reports.
type ManagedInput struct {
	Enrollment          ManagedEnrollmentReceipt               `json:"enrollment"`
	SchemaVersion       int                                    `json:"schemaVersion"`
	Profile             string                                 `json:"profile"`
	RecoverySetID       string                                 `json:"recoverySetId"`
	OccurrenceID        string                                 `json:"occurrenceId"`
	InstanceHome        string                                 `json:"instanceHome"`
	Artifact            compatibility.ReleaseIdentity          `json:"artifact"`
	CredentialsFile     string                                 `json:"credentialsFile"`
	Roles               RuntimeRoles                           `json:"runtimeRoles"`
	Postgres            ManagedPostgresInput                   `json:"postgres"`
	Roots               []ResticConfig                         `json:"roots"`
	Closure             metadata.NativeSnapshotClosureEvidence `json:"closure"`
	PrimaryFence        providerrestore.PrimaryFenceSSHConfig  `json:"primaryFence"`
	EvidenceRoot        string                                 `json:"evidenceRoot"`
	SecretRoot          string                                 `json:"secretRoot"`
	Authority           AuthorityInput                         `json:"authority"`
	ValidationAttemptID string                                 `json:"validationAttemptId"`
	Validator           string                                 `json:"validator"`
	Publisher           string                                 `json:"publisher"`
}

func ReadManagedInput(path string) (ManagedInput, error) {
	value, err := readBoundedManagedPrivateFile(path, 16<<20)
	if err != nil {
		return ManagedInput{}, errors.New("bounded private managed recovery input unavailable")
	}
	var input ManagedInput
	if strictjson.DecodeWithOptions(value, &input, strictjson.Options{MaxBytes: 16 << 20}) != nil || input.SchemaVersion != 1 || input.Profile != providerrestore.ManagedLocalProfile || input.RecoverySetID == "" || input.OccurrenceID == "" || input.ValidationAttemptID == "" || input.Validator == "" || input.Publisher == "" || !filepath.IsAbs(input.InstanceHome) || filepath.Clean(input.InstanceHome) != input.InstanceHome || input.InstanceHome == "/" {
		return ManagedInput{}, errors.New("exact managed-local recovery input required")
	}
	if err := input.Postgres.validateProvider(); err != nil {
		return ManagedInput{}, err
	}
	input.Closure, err = metadata.NativeSnapshotClosureEvidenceFromValues(input.Closure)
	if err != nil {
		return ManagedInput{}, errors.New("exact canonical managed DuckLake closure required")
	}
	return input, nil
}

func (input ManagedInput) Configuration(ctx context.Context) (ManagedConfig, error) {
	if err := ctx.Err(); err != nil {
		return ManagedConfig{}, err
	}
	if err := input.Postgres.validateProvider(); err != nil {
		return ManagedConfig{}, err
	}
	value, err := readBoundedManagedPrivateFile(input.CredentialsFile, maxManagedCredentialsBytes)
	if err != nil {
		return ManagedConfig{}, errors.New("retained private managed credentials unavailable")
	}
	var credentials ManagedCredentials
	if strictjson.DecodeWithOptions(value, &credentials, strictjson.Options{MaxBytes: maxManagedCredentialsBytes}) != nil {
		return ManagedConfig{}, errors.New("retained managed credentials invalid")
	}
	p := input.Postgres
	return ManagedConfig{PostgresProvider: input.Postgres.Provider, Enrollment: input.Enrollment, RecoverySetID: input.RecoverySetID, OccurrenceID: input.OccurrenceID, InstanceHome: input.InstanceHome, Artifact: input.Artifact, Credentials: credentials, Roles: input.Roles, Roots: input.Roots, Closure: input.Closure, PrimaryFence: input.PrimaryFence, EvidenceRoot: input.EvidenceRoot, SecretRoot: input.SecretRoot,
		Postgres: PGBackRestConfig{TargetID: credentials.TargetID, RecoverySetID: input.RecoverySetID, Frontier: p.Frontier, PGBackRest: p.PGBackRest, Bubblewrap: p.Bubblewrap, ConfigFile: p.ConfigFile, ConfigDigest: p.ConfigDigest, Destination: p.Destination},
		Readback: PGNativeReadbackConfig{Native: NativePostgresReadback{MetadataSchema: p.MetadataSchema}, Frontier: p.Frontier, Postgres: p.Postgres, PGControlData: p.PGControlData, PGBackRest: p.PGBackRest, ProviderConfigFile: p.ConfigFile, ProviderConfigDigest: p.ConfigDigest, ServerCertificateFile: p.ServerCertificateFile, ServerCertificateDigest: p.ServerCertificateDigest, ServerKeyFile: p.ServerKeyFile, ServerKeyDigest: p.ServerKeyDigest}}, nil
}
