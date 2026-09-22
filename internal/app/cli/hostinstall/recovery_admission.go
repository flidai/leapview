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
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/app/providerrestore"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/recoveryset"
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
	Probe RecoveryProviderProbe
	Now   func() time.Time
}

func (admission RecoveryAdmission) Admit(ctx context.Context, request RecoveryAdmissionRequest) (RecoveryAdmissionEvidence, error) {
	if strings.TrimSpace(request.ReportPath) == "" || strings.TrimSpace(request.SecretRoot) == "" || strings.TrimSpace(request.OutputPath) == "" || strings.TrimSpace(request.OccurrenceID) == "" || strings.TrimSpace(request.TargetID) == "" || strings.TrimSpace(request.RecoverySetID) == "" || strings.TrimSpace(request.FrontierDigest) == "" || strings.TrimSpace(request.ArtifactIdentity) == "" {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("recovery admission requires report, secret root, output, and all authoritative identities")
	}
	report, err := readProviderRestoreReport(request.ReportPath)
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
	if !result.TLSVerified || result.ControlDatabase == "" || result.DuckLakeDatabase == "" || result.ObjectCount != len(report.Objects) || result.VerifiedAt.IsZero() {
		return RecoveryAdmissionEvidence{}, fmt.Errorf("restored provider probe is incomplete")
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

func readProviderRestoreReport(path string) (providerrestore.Report, error) {
	raw, err := securefs.ReadPrivateFile(path)
	if err != nil {
		return providerrestore.Report{}, err
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
	control, controlTLS, err := probeRecoveryDatabase(ctx, bundle.ControlURL, postgresRoots)
	if err != nil {
		return RecoveryProviderProbeResult{}, err
	}
	ducklake, duckTLS, err := probeRecoveryDatabase(ctx, bundle.DuckLakeURL, postgresRoots)
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
	return RecoveryProviderProbeResult{ControlDatabase: control, DuckLakeDatabase: ducklake, ObjectCount: len(report.Objects), TLSVerified: controlTLS && duckTLS, VerifiedAt: time.Now().UTC()}, nil
}

func probeRecoveryDatabase(ctx context.Context, connectionURL string, roots *x509.CertPool) (string, bool, error) {
	config, err := pgx.ParseConfig(connectionURL)
	if err != nil {
		return "", false, err
	}
	if config.TLSConfig == nil {
		return "", false, fmt.Errorf("PostgreSQL recovery connection did not enable TLS")
	}
	config.TLSConfig.RootCAs = roots
	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return "", false, err
	}
	defer connection.Close(context.Background())
	var database string
	var encrypted bool
	if err := connection.QueryRow(ctx, `SELECT current_database()::text, EXISTS (SELECT 1 FROM pg_stat_ssl WHERE pid=pg_backend_pid() AND ssl)`).Scan(&database, &encrypted); err != nil {
		return "", false, err
	}
	return database, encrypted, nil
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
