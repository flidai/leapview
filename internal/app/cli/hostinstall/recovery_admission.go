package hostinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/app/providerrestore"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/refresh/recovery"
	"github.com/jackc/pgx/v5"
)

const recoveryAdmissionEvidenceSchemaVersion = 1

type RecoveryAdmissionRequest struct {
	ReportPath       string
	SecretRoot       string
	OutputPath       string
	OccurrenceID     string
	TargetID         string
	RecoverySetID    string
	FrontierDigest   string
	ArtifactIdentity string
}

type RecoveryProviderProbe interface {
	Probe(context.Context, providerrestore.Report, providerrestore.CredentialBundle) (RecoveryProviderProbeResult, error)
}

type RecoveryProviderProbeResult struct {
	ControlDatabase  string    `json:"controlDatabase"`
	DuckLakeDatabase string    `json:"duckLakeDatabase"`
	ControlDigest    string    `json:"controlDigest"`
	DuckLakeDigest   string    `json:"duckLakeDigest"`
	ObjectCount      int       `json:"objectCount"`
	TLSVerified      bool      `json:"tlsVerified"`
	VerifiedAt       time.Time `json:"verifiedAt"`
}

type RecoveryAdmissionEvidence struct {
	SchemaVersion    int                                   `json:"schemaVersion"`
	Kind             string                                `json:"kind"`
	Status           string                                `json:"status"`
	OccurrenceID     string                                `json:"occurrenceId"`
	TargetID         string                                `json:"targetId"`
	RecoverySetID    string                                `json:"recoverySetId"`
	FrontierDigest   string                                `json:"frontierDigest"`
	ArtifactIdentity string                                `json:"artifactIdentity"`
	ProviderRefs     []providerrestore.ProviderEndpoint    `json:"providerReferences"`
	SecretReference  providerrestore.SecretBundleReference `json:"secretReference"`
	Probe            RecoveryProviderProbeResult           `json:"providerProbe"`
	AdmittedAt       time.Time                             `json:"admittedAt"`
}

type RecoveryAdmission struct {
	Ledger interface {
		Occurrence(context.Context, string) (recovery.Occurrence, error)
	}
	Probe RecoveryProviderProbe
	Now   func() time.Time
}

func (admission RecoveryAdmission) Admit(ctx context.Context, request RecoveryAdmissionRequest) (RecoveryAdmissionEvidence, error) {
	if strings.TrimSpace(request.OutputPath) != "" {
		if filepath.Clean(request.OutputPath) == filepath.Clean(request.ReportPath) {
			return RecoveryAdmissionEvidence{}, fmt.Errorf("recovery admission output must differ from its report")
		}
		if err := invalidateRecoveryAdmission(request.OutputPath); err != nil {
			return RecoveryAdmissionEvidence{}, fmt.Errorf("invalidate prior recovery admission: %w", err)
		}
	}
	if strings.TrimSpace(request.ReportPath) == "" || strings.TrimSpace(request.SecretRoot) == "" || strings.TrimSpace(request.OutputPath) == "" || strings.TrimSpace(request.OccurrenceID) == "" || strings.TrimSpace(request.TargetID) == "" || strings.TrimSpace(request.RecoverySetID) == "" || strings.TrimSpace(request.FrontierDigest) == "" || strings.TrimSpace(request.ArtifactIdentity) == "" {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("recovery admission requires report, secret root, output, and all authoritative identities")
	}
	if admission.Ledger == nil {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("PostgreSQL recovery ledger is required")
	}
	occurrence, err := admission.Ledger.Occurrence(ctx, request.OccurrenceID)
	if err != nil {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("read authoritative recovery occurrence: %w", err)
	}
	if occurrence.ID != request.OccurrenceID || occurrence.Status != recovery.StatusSucceeded || occurrence.Operation != recovery.OperationRestore || occurrence.TargetScope != request.TargetID || occurrence.ArtifactIdentity != request.ArtifactIdentity || len(occurrence.Evidence) != 1 || occurrence.Evidence[0].Kind != "provider-restore" {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("authoritative recovery occurrence does not match admission")
	}
	report, err := readProviderRestoreReport(request.ReportPath, occurrence.Evidence[0])
	if err != nil {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("read provider restore report: %w", err)
	}
	expected := providerrestore.HandoffExpectations{
		OccurrenceID: request.OccurrenceID, TargetID: request.TargetID,
		RecoverySetID: request.RecoverySetID, FrontierDigest: request.FrontierDigest,
		ArtifactIdentity: request.ArtifactIdentity,
	}
	if err := providerrestore.ValidateHandoffReport(report, expected); err != nil {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("validate authoritative provider handoff: %w", err)
	}
	if !report.Verification.Ready || !report.Verification.ObjectsConsistent || report.Admission.PublishedSetID != report.RecoverySetID || report.Admission.PublishedStatus != recoveryset.StatusPublished || report.Admission.ValidationAttemptID == "" || report.Admission.ValidationDigest == "" || report.Admission.PublishedAt.IsZero() {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("provider restore is not authoritatively verified and published")
	}
	bundle, err := (providerrestore.FileSecretBundleStore{Root: request.SecretRoot}).Load(ctx, report.Handoff.Secrets)
	if err != nil {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("resolve provisioned recovery secrets: %w", err)
	}
	if err := providerrestore.ValidateCredentialBundleForHandoff(report.Handoff, bundle); err != nil {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("bind recovery credentials to handoff: %w", err)
	}
	probe := admission.Probe
	if probe == nil {
		probe = productionRecoveryProviderProbe{}
	}
	result, err := probe.Probe(ctx, report, bundle)
	if err != nil {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("probe restored providers: %w", err)
	}
	if !result.TLSVerified || result.ControlDatabase == "" || result.DuckLakeDatabase == "" || result.ObjectCount != len(report.Objects) || result.VerifiedAt.IsZero() || result.ControlDigest == "" || result.DuckLakeDigest == "" {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("restored provider probe is incomplete")
	}
	if result.ControlDigest != report.Verification.ControlStateDigest || result.DuckLakeDigest != report.Verification.DuckLakeStateDigest {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("live PostgreSQL state does not match the authoritative restore digests")
	}
	now := admission.Now
	if now == nil {
		now = time.Now
	}
	evidence := RecoveryAdmissionEvidence{
		SchemaVersion: recoveryAdmissionEvidenceSchemaVersion, Kind: "leapview/replacement-host-recovery-admission", Status: "admitted",
		OccurrenceID: report.OccurrenceID, TargetID: report.TargetID, RecoverySetID: report.RecoverySetID,
		FrontierDigest: report.FrontierDigest, ArtifactIdentity: report.Handoff.Artifact.Image,
		ProviderRefs: report.Handoff.Providers, SecretReference: report.Handoff.Secrets,
		Probe: result, AdmittedAt: now().UTC(),
	}
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return RecoveryAdmissionEvidence{}, err
	}
	encoded = append(encoded, '\n')
	if err := securefs.WritePrivateFileAtomic(request.OutputPath, encoded); err != nil {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("persist recovery admission: %w", err)
	}
	return evidence, nil
}

func invalidateRecoveryAdmission(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("admission output is a directory")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func readProviderRestoreReport(path string, reference recovery.EvidenceReference) (providerrestore.Report, error) {
	raw, err := securefs.ReadPrivateFile(path)
	if err != nil {
		return providerrestore.Report{}, err
	}
	canonical, err := recovery.CanonicalEvidenceReferences([]recovery.EvidenceReference{reference})
	if err != nil || len(canonical) != 1 || reference.Kind != "provider-restore" {
		return providerrestore.Report{}, fmt.Errorf("authoritative provider evidence reference is invalid")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != reference.SHA256 {
		return providerrestore.Report{}, fmt.Errorf("provider restore report does not match the ledger evidence digest")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var report providerrestore.Report
	if err := decoder.Decode(&report); err != nil {
		return providerrestore.Report{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return providerrestore.Report{}, fmt.Errorf("provider restore report contains trailing data")
	}
	return report, nil
}

type productionRecoveryProviderProbe struct{}

func (productionRecoveryProviderProbe) Probe(ctx context.Context, report providerrestore.Report, bundle providerrestore.CredentialBundle) (RecoveryProviderProbeResult, error) {
	postgresRoots, err := certificatePool(bundle.PostgresRootCA)
	if err != nil {
		return RecoveryProviderProbeResult{}, err
	}
	control, controlDigest, controlTLS, err := probeRecoveryDatabase(ctx, bundle.ControlURL, postgresRoots, recoveryset.DatabaseControl)
	if err != nil {
		return RecoveryProviderProbeResult{}, err
	}
	ducklake, ducklakeDigest, duckTLS, err := probeRecoveryDatabase(ctx, bundle.DuckLakeURL, postgresRoots, recoveryset.DatabaseDuckLake)
	if err != nil {
		return RecoveryProviderProbeResult{}, err
	}
	objectRoots, err := certificatePool(bundle.ObjectRootCA)
	if err != nil {
		return RecoveryProviderProbeResult{}, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: objectRoots}
	client := s3.New(s3.Options{
		Region: bundle.ObjectRegion, BaseEndpoint: aws.String(bundle.ObjectEndpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(bundle.ObjectAccessKey, bundle.ObjectSecretKey, ""),
		HTTPClient:  &http.Client{Transport: transport, Timeout: 15 * time.Second},
	})
	objectEndpoint := handoffProvider(report.Handoff.Providers, "objects")
	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &objectEndpoint.Bucket}); err != nil {
		return RecoveryProviderProbeResult{}, fmt.Errorf("reach restored object bucket: %w", err)
	}
	for _, object := range report.Objects {
		parsed, parseErr := url.Parse(object.URI)
		if parseErr != nil || parsed.Scheme != "s3" || parsed.Host != objectEndpoint.Bucket {
			return RecoveryProviderProbeResult{}, fmt.Errorf("restored object identity is inconsistent")
		}
		key, version := strings.TrimPrefix(parsed.Path, "/"), object.ObservedVersionID
		response, getErr := client.GetObject(ctx, &s3.GetObjectInput{Bucket: &objectEndpoint.Bucket, Key: &key, VersionId: &version})
		if getErr != nil {
			return RecoveryProviderProbeResult{}, fmt.Errorf("read restored object version: %w", getErr)
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, response.Body)
		closeErr := response.Body.Close()
		observed := hex.EncodeToString(digest.Sum(nil))
		expected := strings.TrimPrefix(object.Digest, "sha256:")
		if copyErr != nil || closeErr != nil || observed != expected {
			return RecoveryProviderProbeResult{}, fmt.Errorf("restored object digest mismatch: %w", errors.Join(copyErr, closeErr))
		}
	}
	return RecoveryProviderProbeResult{ControlDatabase: control, DuckLakeDatabase: ducklake, ControlDigest: controlDigest, DuckLakeDigest: ducklakeDigest, ObjectCount: len(report.Objects), TLSVerified: controlTLS && duckTLS, VerifiedAt: time.Now().UTC()}, nil
}

func probeRecoveryDatabase(ctx context.Context, connectionURL string, roots *x509.CertPool, role recoveryset.DatabaseRole) (string, string, bool, error) {
	config, err := pgx.ParseConfig(connectionURL)
	if err != nil {
		return "", "", false, err
	}
	if config.TLSConfig == nil {
		return "", "", false, fmt.Errorf("PostgreSQL recovery connection did not enable TLS")
	}
	config.TLSConfig.RootCAs = roots
	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return "", "", false, err
	}
	defer connection.Close(context.Background())
	var database string
	var encrypted bool
	if err := connection.QueryRow(ctx, `SELECT current_database()::text, EXISTS (SELECT 1 FROM pg_stat_ssl WHERE pid=pg_backend_pid() AND ssl)`).Scan(&database, &encrypted); err != nil {
		return "", "", false, err
	}
	digest, err := RecoveryDatabaseStateDigest(ctx, connection, role)
	if err != nil {
		return "", "", false, err
	}
	return database, digest, encrypted, nil
}

// RecoveryDatabaseStateDigest uses the same representative state projection as
// the FAI-981 PostgreSQL restore qualification.
func RecoveryDatabaseStateDigest(ctx context.Context, database interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, role recoveryset.DatabaseRole) (string, error) {
	query := `SELECT id::text,state_key,state_value FROM application_state ORDER BY id`
	switch role {
	case recoveryset.DatabaseControl:
	case recoveryset.DatabaseDuckLake:
		query = `
	SELECT 'metadata',key,value FROM ducklake_catalog.ducklake_metadata
	UNION ALL SELECT 'snapshot',snapshot_id::text,catalog_version::text FROM ducklake_catalog.ducklake_snapshot
	UNION ALL SELECT 'object',object_uri,object_version||':'||object_digest FROM ducklake_catalog.ducklake_data_file
	ORDER BY 1,2,3`
	default:
		return "", fmt.Errorf("unknown restored database role %q", role)
	}
	rows, err := database.Query(ctx, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var first, second, third string
		if err := rows.Scan(&first, &second, &third); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\n", first, second, third)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func certificatePool(raw string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(raw)) {
		return nil, fmt.Errorf("recovery TLS root certificate is invalid")
	}
	return pool, nil
}

func handoffProvider(providers []providerrestore.ProviderEndpoint, role string) providerrestore.ProviderEndpoint {
	for _, provider := range providers {
		if provider.Role == role {
			return provider
		}
	}
	return providerrestore.ProviderEndpoint{}
}

func recoveryAdmissionContainsSecret(evidence RecoveryAdmissionEvidence, secrets ...string) bool {
	raw, _ := json.Marshal(evidence)
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			return true
		}
	}
	return false
}
