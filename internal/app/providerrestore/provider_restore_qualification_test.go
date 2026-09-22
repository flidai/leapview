//go:build fai981qualification

package providerrestore_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/cli/hostinstall"
	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/platform/compatibility"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	postgresmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/recoveryset"
	recoverypg "github.com/flidai/leapview/internal/recoveryset/postgres"
	refreshpg "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/flidai/leapview/internal/refresh/recovery"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	dockercontainer "github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	qualificationMinIOImage    = "quay.io/minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"
	qualificationConsumerImage = "debian:bookworm-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171"
)

func TestFAI981CoordinatedProviderRestoreQualification(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", "1")
	evidenceRoot := strings.TrimSpace(os.Getenv("LEAPVIEW_TEST_FAI981_PROVIDER_RESTORE_EVIDENCE_DIR"))
	if evidenceRoot == "" {
		t.Fatal("LEAPVIEW_TEST_FAI981_PROVIDER_RESTORE_EVIDENCE_DIR is required")
	}
	privateRoot := strings.TrimSpace(os.Getenv("LEAPVIEW_TEST_FAI981_HANDOFF_PRIVATE_DIR"))
	if privateRoot == "" {
		t.Fatal("LEAPVIEW_TEST_FAI981_HANDOFF_PRIVATE_DIR is required")
	}
	manifestPath := requiredQualificationEnv(t, "LEAPVIEW_TEST_FAI981_RESOURCE_MANIFEST")
	runID := uuid.NewString()
	manifest := providerrestore.RetainedResourceManifestStore{Path: manifestPath}
	if err := manifest.Start(runID); err != nil {
		t.Fatal(err)
	}
	recoveryNetwork := startQualificationNetwork(t, manifest, runID)
	providerHost := requiredQualificationEnv(t, "LEAPVIEW_TEST_FAI981_PROVIDER_HOST")
	tlsMaterial := qualificationTLS(t, providerHost)
	artifact := qualificationArtifact(t)
	objects := startQualificationObjects(t, recoveryNetwork, manifest, runID, providerHost, tlsMaterial)
	objectPoints := objects.seedAndDamage(t)
	postgres := startQualificationPostgres(t, recoveryNetwork, manifest, runID, providerHost, tlsMaterial)
	backups := postgres.seedBackupAndDamage(t, objectPoints)
	authorityPool := postgres.createAuthorityDatabase(t)

	set := qualificationRecoverySet(t, postgres.clusterIdentity, backups, objects, objectPoints)
	canonicalSet, err := set.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonicalSet, []byte(`\u0000`)) {
		t.Fatalf("recovery set contains a NUL string: %s", canonicalSet)
	}
	setRepository := recoverypg.New(authorityPool)
	created, err := setRepository.Create(t.Context(), set)
	if err != nil {
		t.Fatalf("create exact recovery set: %v", err)
	}
	ledger := refreshpg.NewRecoveryLedger(authorityPool)
	occurrence := qualificationOccurrence(t, ledger, created, artifact.Image)
	store := providerrestore.FileEvidenceStore{Root: filepath.Join(evidenceRoot, "checkpoints")}
	databaseProvider := &qualificationDatabaseProvider{fixture: postgres, backups: backups, restoredURLs: map[recoveryset.DatabaseRole]string{}}
	objectProvider := &qualificationObjectProvider{fixture: objects, points: objectPoints}
	verifier := &qualificationVerifier{databases: databaseProvider, objects: objects, points: objectPoints, expected: backups}
	coordinator, err := providerrestore.New(providerrestore.Dependencies{
		Ledger: ledger, Sets: setRepository, Databases: databaseProvider, Objects: objectProvider,
		Verifier: verifier, Evidence: store,
		Handoff: &qualificationHandoffProvider{artifact: artifact, postgres: postgres, databases: databaseProvider, objects: objects, private: providerrestore.FileSecretBundleStore{Root: privateRoot}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := providerrestore.Request{
		OccurrenceID: occurrence.ID, Fence: occurrence.Fence, RecoverySetID: created.ID,
		TargetID: created.Delivery.TargetID, ValidationAttemptID: uuid.NewString(),
		Validator: "fai981-provider-validator", Publisher: "fai981-provider-publisher",
	}
	report, err := coordinator.Run(t.Context(), request)
	if err != nil {
		t.Fatalf("coordinate physical provider restore: %v", err)
	}
	finalOccurrence, err := ledger.Occurrence(t.Context(), occurrence.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := ledger.Attempts(t.Context(), occurrence.ID)
	if err != nil {
		t.Fatal(err)
	}
	published, err := setRepository.ReadExact(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertQualificationResult(t, report, finalOccurrence, published, objectPoints)

	summary := struct {
		SchemaVersion int                     `json:"schemaVersion"`
		Qualification string                  `json:"qualification"`
		Providers     map[string]string       `json:"providers"`
		Report        providerrestore.Report  `json:"report"`
		Occurrence    recovery.Occurrence     `json:"occurrence"`
		Attempts      []recovery.Attempt      `json:"attempts"`
		RecoverySet   recoveryset.RecoverySet `json:"recoverySet"`
	}{
		SchemaVersion: 1, Qualification: "FAI-981 coordinated physical provider restoration",
		Providers: map[string]string{
			"postgresql": "PostgreSQL 18 physical pg_basebackup restored as an independent server in an isolated provider container",
			"objects":    "MinIO S3 immutable-version recovery in an isolated provider container",
		},
		Report: report, Occurrence: finalOccurrence, Attempts: attempts, RecoverySet: published,
	}
	writeQualificationEvidence(t, evidenceRoot, summary, postgres.password, objects.user, objects.secret)
	t.Logf("FAI-981 coordinated provider restore evidence: %s", filepath.Join(evidenceRoot, "coordinated-provider-restore.json"))
}

func TestFAI981DownstreamHandoffSurvivesProducerExit(t *testing.T) {
	evidenceRoot := requiredQualificationEnv(t, "LEAPVIEW_TEST_FAI981_PROVIDER_RESTORE_EVIDENCE_DIR")
	privateRoot := requiredQualificationEnv(t, "LEAPVIEW_TEST_FAI981_HANDOFF_PRIVATE_DIR")
	raw, err := os.ReadFile(filepath.Join(evidenceRoot, "coordinated-provider-restore.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Report     providerrestore.Report `json:"report"`
		Occurrence recovery.Occurrence    `json:"occurrence"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Occurrence.Evidence) != 1 {
		t.Fatalf("provider evidence references = %#v", summary.Occurrence.Evidence)
	}
	report := summary.Report
	if os.Getenv("LEAPVIEW_TEST_FAI981_ISOLATED_CONSUMER") == "" {
		report, err = (providerrestore.FileEvidenceStore{Root: filepath.Join(evidenceRoot, "checkpoints")}).Load(t.Context(), summary.Occurrence.Evidence[0])
		if err != nil {
			t.Fatal(err)
		}
	}
	expected := providerrestore.HandoffExpectations{OccurrenceID: summary.Occurrence.ID, TargetID: summary.Occurrence.TargetScope, RecoverySetID: report.RecoverySetID, FrontierDigest: report.FrontierDigest, ArtifactIdentity: summary.Occurrence.ArtifactIdentity}
	if err := providerrestore.ValidateHandoffReport(report, expected); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("LEAPVIEW_TEST_FAI981_ISOLATED_CONSUMER") == "" {
		runIsolatedHandoffConsumer(t, evidenceRoot, privateRoot, report)
		return
	}
	bundle, err := (providerrestore.FileSecretBundleStore{Root: privateRoot}).Load(t.Context(), report.Handoff.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	privateBundlePath := filepath.Join(privateRoot, report.Handoff.Secrets.SHA256+".json")
	if relative, err := filepath.Rel(evidenceRoot, privateBundlePath); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatal("credential bundle is inside the published evidence tree")
	}
	reportPath := filepath.Join(t.TempDir(), "provider-restore-report.json")
	reportJSON, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, reportJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	admission, err := (hostinstall.RecoveryAdmission{}).Admit(t.Context(), hostinstall.RecoveryAdmissionRequest{
		ReportPath: reportPath, SecretRoot: privateRoot, OutputPath: "/tmp/recovery-admission.json",
		OccurrenceID: report.OccurrenceID, TargetID: report.TargetID, RecoverySetID: report.RecoverySetID,
		FrontierDigest: report.FrontierDigest, ArtifactIdentity: report.Handoff.Artifact.Image,
	})
	if err != nil {
		t.Fatalf("replacement-host preactivation admission: %v", err)
	}
	if admission.Status != "admitted" || !admission.Probe.TLSVerified || admission.Probe.ObjectCount != len(report.Objects) {
		t.Fatalf("replacement-host preactivation admission incomplete: %#v", admission)
	}
	if os.Getenv("LEAPVIEW_TEST_FAI981_ISOLATED_CONSUMER") == "" {
		for _, resource := range report.Handoff.Providers {
			output, inspectErr := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", resource.ResourceID).CombinedOutput()
			if inspectErr != nil || strings.TrimSpace(string(output)) != "true" {
				t.Fatalf("retained provider %s is not running: %v: %s", resource.Role, inspectErr, output)
			}
		}
	}
	control := openQualificationTLSPool(t, bundle.ControlURL, bundle.PostgresRootCA)
	controlDigest := qualificationDatabaseStateDigest(t, control, recoveryset.DatabaseControl)
	ducklake := openQualificationTLSPool(t, bundle.DuckLakeURL, bundle.PostgresRootCA)
	duckDigest := qualificationDatabaseStateDigest(t, ducklake, recoveryset.DatabaseDuckLake)
	if controlDigest != report.Verification.ControlStateDigest || duckDigest != report.Verification.DuckLakeStateDigest {
		t.Fatalf("downstream database state mismatch: %s/%s", controlDigest, duckDigest)
	}
	client := awss3.New(awss3.Options{Region: bundle.ObjectRegion, BaseEndpoint: aws.String(bundle.ObjectEndpoint), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(bundle.ObjectAccessKey, bundle.ObjectSecretKey, ""), RetryMaxAttempts: 1, HTTPClient: qualificationTLSHTTPClient(t, bundle.ObjectRootCA)})
	for _, object := range report.Objects {
		parsed, parseErr := parseObjectURI(object.URI)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		key, version := strings.TrimPrefix(parsed.Path, "/"), object.ObservedVersionID
		response, getErr := client.GetObject(t.Context(), &awss3.GetObjectInput{Bucket: aws.String(parsed.Host), Key: &key, VersionId: &version})
		if getErr != nil {
			t.Fatal(getErr)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || digestBytes(body) != object.Digest {
			t.Fatalf("downstream object %s mismatch: read=%v close=%v", object.Kind, readErr, closeErr)
		}
	}
	if os.Getenv("LEAPVIEW_TEST_FAI981_ISOLATED_CONSUMER") == "" {
		verifyQualificationArtifact(t, report.Handoff.Artifact)
	}
	t.Logf("FAI-981 downstream handoff independently loaded from %s", summary.Occurrence.Evidence[0].URI)
}

type qualificationHandoffProvider struct {
	artifact  compatibility.ReleaseIdentity
	postgres  *qualificationPostgresFixture
	databases *qualificationDatabaseProvider
	objects   *qualificationObjects
	private   providerrestore.FileSecretBundleStore
}

func (provider *qualificationHandoffProvider) CreateHandoff(ctx context.Context, request providerrestore.HandoffRequest) (providerrestore.ReplacementHandoff, error) {
	var controlURL, duckLakeURL string
	for _, database := range request.Databases {
		switch database.DatabaseRole {
		case recoveryset.DatabaseControl:
			controlURL = provider.postgres.consumerDatabaseURL(database.DatabaseIdentity)
		case recoveryset.DatabaseDuckLake:
			duckLakeURL = provider.postgres.consumerDatabaseURL(database.DatabaseIdentity)
		}
	}
	if controlURL == "" || duckLakeURL == "" {
		return providerrestore.ReplacementHandoff{}, fmt.Errorf("restored PostgreSQL endpoints are unavailable")
	}
	secretReference, err := provider.private.Save(ctx, providerrestore.CredentialBundle{SchemaVersion: 2, ControlURL: controlURL, DuckLakeURL: duckLakeURL, ObjectEndpoint: provider.objects.endpoint, ObjectRegion: "us-east-1", ObjectAccessKey: provider.objects.user, ObjectSecretKey: provider.objects.secret, PostgresRootCA: provider.postgres.rootCA, ObjectRootCA: provider.objects.rootCA})
	if err != nil {
		return providerrestore.ReplacementHandoff{}, err
	}
	controlEndpoint, controlDatabase, err := publicDatabaseEndpoint(controlURL)
	if err != nil {
		return providerrestore.ReplacementHandoff{}, err
	}
	duckEndpoint, duckDatabase, err := publicDatabaseEndpoint(duckLakeURL)
	if err != nil {
		return providerrestore.ReplacementHandoff{}, err
	}
	return providerrestore.ReplacementHandoff{
		SchemaVersion: providerrestore.HandoffSchemaVersion, Kind: providerrestore.HandoffKind, Status: providerrestore.HandoffAvailable,
		RecoverySetID: request.Set.ID, FrontierDigest: request.Set.FrontierDigest, TargetID: request.Set.Delivery.TargetID, Artifact: provider.artifact,
		Providers: []providerrestore.ProviderEndpoint{
			{Role: "control", Provider: "postgresql-physical-basebackup", ResourceID: provider.postgres.resourceID, Endpoint: controlEndpoint, Database: controlDatabase, CredentialSecretKey: "postgres.control.url", TLSRootCASecretKey: "postgres.root-ca"},
			{Role: "ducklake", Provider: "postgresql-physical-basebackup", ResourceID: provider.postgres.resourceID, Endpoint: duckEndpoint, Database: duckDatabase, CredentialSecretKey: "postgres.ducklake.url", TLSRootCASecretKey: "postgres.root-ca"},
			{Role: "objects", Provider: "minio-s3-versioning", ResourceID: provider.objects.resourceID, Endpoint: provider.objects.endpoint, Region: "us-east-1", Bucket: provider.objects.bucket, CredentialSecretKey: "object.credentials", TLSRootCASecretKey: "object.root-ca"},
		},
		Secrets: secretReference, AvailableAt: time.Now().UTC(),
	}, nil
}

func qualificationArtifact(t *testing.T) compatibility.ReleaseIdentity {
	t.Helper()
	identity := compatibility.ReleaseIdentity{Image: requiredQualificationEnv(t, "LEAPVIEW_TEST_FAI981_HANDOFF_IMAGE"), SourceRevision: requiredQualificationEnv(t, "LEAPVIEW_TEST_FAI981_HANDOFF_REVISION"), Distribution: "oci", Platform: "linux/amd64"}
	verifyQualificationArtifact(t, identity)
	labelsRaw, err := exec.Command("docker", "image", "inspect", "--format", "{{json .Config.Labels}}", identity.Image).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect runnable handoff artifact: %v: %s", err, labelsRaw)
	}
	var labels map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(labelsRaw), &labels); err != nil {
		t.Fatal(err)
	}
	identity.Version = labels["org.opencontainers.image.version"]
	if labels["org.opencontainers.image.revision"] != identity.SourceRevision || identity.Version == "" {
		t.Fatalf("artifact labels revision=%q version=%q", labels["org.opencontainers.image.revision"], identity.Version)
	}
	return identity
}

func verifyQualificationArtifact(t *testing.T, identity compatibility.ReleaseIdentity) {
	t.Helper()
	if output, err := exec.Command("docker", "pull", identity.Image).CombinedOutput(); err != nil {
		t.Fatalf("pull exact handoff artifact: %v: %s", err, output)
	}
	output, err := exec.Command("docker", "image", "inspect", "--format", "{{index .Config.Labels \"org.opencontainers.image.revision\"}}", identity.Image).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != identity.SourceRevision {
		t.Fatalf("handoff artifact source revision = %q, error=%v; want %q", strings.TrimSpace(string(output)), err, identity.SourceRevision)
	}
}

func publicDatabaseEndpoint(value string) (string, string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User == nil || parsed.Hostname() == "" {
		return "", "", fmt.Errorf("invalid private PostgreSQL URL")
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	parsed.User, parsed.Path = nil, ""
	return parsed.String(), database, nil
}

func requiredQualificationEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}

func startQualificationNetwork(t *testing.T, manifest providerrestore.RetainedResourceManifestStore, runID string) string {
	t.Helper()
	name := "leapview-fai981-recovery-" + strings.ReplaceAll(runID, "-", "")
	output, err := exec.Command("docker", "network", "create", "--internal", name).CombinedOutput()
	if err != nil {
		t.Fatalf("create isolated recovery network: %v: %s", err, output)
	}
	if err := manifest.Record(runID, providerrestore.RetainedResource{Kind: "network", ID: name}); err != nil {
		_, _ = exec.Command("docker", "network", "rm", name).CombinedOutput()
		t.Fatal(err)
	}
	return name
}

func randomQualificationCredential(t *testing.T, byteCount int) string {
	t.Helper()
	raw := make([]byte, byteCount)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

type qualificationTLSMaterial struct {
	caCert, serverCert, serverKey string
}

func qualificationTLS(t *testing.T, host string) qualificationTLSMaterial {
	t.Helper()
	if !providerHostIdentityValid(host) {
		t.Fatalf("LEAPVIEW_TEST_FAI981_PROVIDER_HOST %q is not a replacement-host-routable IP or DNS identity", host)
	}
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "LeapView FAI-981 qualification root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
	if address := net.ParseIP(host); address != nil {
		server.IPAddresses = []net.IP{address}
	} else {
		server.DNSNames = []string{host}
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, server, ca, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	return qualificationTLSMaterial{
		caCert:     string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		serverCert: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})),
		serverKey:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})),
	}
}

func providerHostIdentityValid(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || !strings.Contains(host, ".") {
		return false
	}
	if address := net.ParseIP(host); address != nil {
		return !address.IsLoopback() && !address.IsUnspecified() && !address.IsLinkLocalUnicast()
	}
	return true
}

func qualificationTLSHTTPClient(t *testing.T, rootCA string) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(rootCA)) {
		t.Fatal("qualification TLS root is invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second}
}

func assertLoopbackPortBindings(t *testing.T, containerID string) {
	t.Helper()
	output, err := exec.Command("docker", "inspect", "--format", "{{json .HostConfig.PortBindings}}", containerID).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect retained provider listener: %v: %s", err, output)
	}
	var bindings map[string][]struct {
		HostIP string `json:"HostIp"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output), &bindings); err != nil {
		t.Fatal(err)
	}
	if len(bindings) == 0 {
		t.Fatal("retained provider has no loopback listener for producer verification")
	}
	for port, values := range bindings {
		if len(values) == 0 {
			t.Fatalf("retained provider port %s has no binding", port)
		}
		for _, value := range values {
			if value.HostIP != "127.0.0.1" {
				t.Fatalf("retained provider port %s is bound to %q, want loopback", port, value.HostIP)
			}
		}
	}
}

func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(port)
}

func persistResourceAfterCreate(manifest providerrestore.RetainedResourceManifestStore, runID string) testcontainers.ContainerLifecycleHooks {
	return testcontainers.ContainerLifecycleHooks{PostCreates: []testcontainers.ContainerHook{
		func(_ context.Context, container testcontainers.Container) error {
			return manifest.Record(runID, providerrestore.RetainedResource{Kind: "container", ID: container.GetContainerID()})
		},
	}}
}

func runIsolatedHandoffConsumer(t *testing.T, evidenceRoot, privateRoot string, report providerrestore.Report) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sourceBundle, err := (providerrestore.FileSecretBundleStore{Root: privateRoot}).SourcePath(report.Handoff.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			Image:      qualificationConsumerImage,
			Entrypoint: []string{"sleep"}, Cmd: []string{"300"},
			Env: map[string]string{
				"LEAPVIEW_TEST_FAI981_ISOLATED_CONSUMER":             "1",
				"LEAPVIEW_TEST_FAI981_PROVIDER_RESTORE_EVIDENCE_DIR": "/tmp",
				"LEAPVIEW_TEST_FAI981_HANDOFF_PRIVATE_DIR":           "/run/leapview/recovery",
			},
		},
	})
	if err != nil {
		t.Fatalf("start isolated handoff consumer: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(consumer) })
	assertIsolatedConsumer(t, consumer.GetContainerID(), "bridge")
	if code, _, err := consumer.Exec(t.Context(), []string{"mkdir", "-p", "/run/leapview/recovery"}, tcexec.Multiplexed()); err != nil || code != 0 {
		t.Fatalf("prepare consumer secret directory: exit=%d err=%v", code, err)
	}
	if err := consumer.CopyFileToContainer(t.Context(), executable, "/tmp/fai981-consumer", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := consumer.CopyFileToContainer(t.Context(), filepath.Join(evidenceRoot, "coordinated-provider-restore.json"), "/tmp/coordinated-provider-restore.json", 0o600); err != nil {
		t.Fatal(err)
	}
	consumerSecretPath := filepath.Join("/run/leapview/recovery", report.Handoff.Secrets.SHA256+".json")
	if err := consumer.CopyFileToContainer(t.Context(), sourceBundle, consumerSecretPath, 0o600); err != nil {
		t.Fatal(err)
	}
	exitCode, output, err := consumer.Exec(t.Context(), []string{"/tmp/fai981-consumer", "-test.run=^TestFAI981DownstreamHandoffSurvivesProducerExit$", "-test.v"}, tcexec.Multiplexed())
	raw, readErr := io.ReadAll(output)
	if err != nil || readErr != nil || exitCode != 0 {
		t.Fatalf("isolated handoff consumer failed: exit=%d exec=%v read=%v output=%s", exitCode, err, readErr, raw)
	}
	admissionReader, err := consumer.CopyFileFromContainer(t.Context(), "/tmp/recovery-admission.json")
	if err != nil {
		t.Fatalf("copy preactivation admission evidence: %v", err)
	}
	admissionJSON, readErr := io.ReadAll(admissionReader)
	closeErr := admissionReader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read preactivation admission evidence: %v", errorsJoin(readErr, closeErr))
	}
	if err := os.WriteFile(filepath.Join(evidenceRoot, "replacement-host-preactivation-admission.json"), admissionJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	assertQualificationEvidenceCredentialFree(t, evidenceRoot)
	t.Log("isolated handoff consumer used the default bridge without the producer recovery network or filesystem mounts")
}

func assertIsolatedConsumer(t *testing.T, containerID, networkName string) {
	t.Helper()
	output, err := exec.Command("docker", "inspect", "--format", "{{json .NetworkSettings.Networks}}\n{{json .Mounts}}", containerID).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect isolated consumer: %v: %s", err, output)
	}
	parts := bytes.SplitN(bytes.TrimSpace(output), []byte("\n"), 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected consumer inspection: %s", output)
	}
	var networks map[string]json.RawMessage
	var mounts []json.RawMessage
	if err := json.Unmarshal(parts[0], &networks); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(parts[1], &mounts); err != nil {
		t.Fatal(err)
	}
	if len(networks) != 1 || networks[networkName] == nil {
		t.Fatalf("consumer networks = %#v, want only %s", networks, networkName)
	}
	if len(mounts) != 0 {
		t.Fatalf("consumer unexpectedly has producer filesystem mounts: %#v", mounts)
	}
}

type qualificationPostgresFixture struct {
	container       *tcpostgres.PostgresContainer
	resourceID      string
	admin           *pgxpool.Pool
	password        string
	clusterIdentity string
	timeline        string
	backupIdentity  string
	providerHost    string
	rootCA          string
}

type qualificationBackup struct {
	point       recoveryset.ClusterRecoveryPoint
	stateDigest string
}

func startQualificationPostgres(t *testing.T, networkName string, manifest providerrestore.RetainedResourceManifestStore, runID, providerHost string, material qualificationTLSMaterial) *qualificationPostgresFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	containerName := "leapview-fai981-postgres-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	password := randomQualificationCredential(t, 32)
	if password == "fai981-provider-secret" || len(password) < 64 || password == randomQualificationCredential(t, 32) {
		t.Fatal("PostgreSQL qualification credential is not unpredictable")
	}
	primaryPort, restorePort := freeLoopbackPort(t), freeLoopbackPort(t)
	container, err := tcpostgres.Run(ctx, postgrestest.PostgreSQL18Image,
		tcpostgres.WithDatabase("postgres"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword(password),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{Reader: strings.NewReader(material.caCert), ContainerFilePath: "/etc/fai981-tls/ca.crt", FileMode: 0o600},
			testcontainers.ContainerFile{Reader: strings.NewReader(material.serverCert), ContainerFilePath: "/etc/fai981-tls/server.crt", FileMode: 0o600},
			testcontainers.ContainerFile{Reader: strings.NewReader(material.serverKey), ContainerFilePath: "/etc/fai981-tls/server.key", FileMode: 0o600},
		),
		testcontainers.WithReuseByName(containerName),
		testcontainers.WithCmd("postgres"),
		testcontainers.WithExposedPorts("55432/tcp"),
		tcnetwork.WithNetworkName([]string{"fai981-postgres"}, networkName),
		tcnetwork.WithBridgeNetwork(),
		testcontainers.WithAdditionalLifecycleHooks(persistResourceAfterCreate(manifest, runID)),
		testcontainers.WithHostConfigModifier(func(config *dockercontainer.HostConfig) {
			config.PortBindings = dockernetwork.PortMap{
				dockernetwork.MustParsePort("5432/tcp"):  {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: primaryPort}},
				dockernetwork.MustParsePort("55432/tcp"): {{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: restorePort}},
			}
		}),
		testcontainers.WithTmpfs(map[string]string{"/var/lib/postgresql": "rw,size=1g", "/tmp": "rw,size=512m"}),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		t.Fatalf("start required PostgreSQL restore provider: %v", err)
	}
	if err := manifest.Record(runID, providerrestore.RetainedResource{Kind: "container", ID: container.GetContainerID()}); err != nil {
		_ = testcontainers.TerminateContainer(container)
		t.Fatal(err)
	}
	runContainerCommand(t, container, "chown", "postgres:postgres", "/etc/fai981-tls/server.key", "/etc/fai981-tls/server.crt", "/etc/fai981-tls/ca.crt")
	runContainerCommand(t, container, "chmod", "0600", "/etc/fai981-tls/server.key")
	adminURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	var systemID, timeline string
	if err := admin.QueryRow(ctx, `SELECT system_identifier::text FROM pg_control_system()`).Scan(&systemID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT timeline_id::text FROM pg_control_checkpoint()`).Scan(&timeline); err != nil {
		t.Fatal(err)
	}
	return &qualificationPostgresFixture{container: container, resourceID: container.GetContainerID(), admin: admin, password: password, clusterIdentity: "postgres-system:" + systemID, timeline: timeline, providerHost: providerHost, rootCA: material.caCert}
}

func (fixture *qualificationPostgresFixture) seedBackupAndDamage(t *testing.T, objects map[string]qualificationObjectPoint) map[recoveryset.DatabaseRole]qualificationBackup {
	t.Helper()
	for _, name := range []string{"fai981_control_source", "fai981_ducklake_source"} {
		fixture.createDatabase(t, name)
	}
	controlURL := fixture.databaseURL(t, "fai981_control_source")
	control := openQualificationPool(t, controlURL)
	for _, statement := range []string{
		`CREATE TABLE application_state(id bigint PRIMARY KEY, state_key text NOT NULL, state_value text NOT NULL)`,
		`INSERT INTO application_state VALUES (1,'tenant','tenant-a'),(2,'dashboard','orders-dashboard'),(3,'policy','governed')`,
	} {
		if _, err := control.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	duckURL := fixture.databaseURL(t, "fai981_ducklake_source")
	duck := openQualificationPool(t, duckURL)
	duckRoot := objects[recoveryset.ObjectRootDuckLake]
	artifactRoot := objects[recoveryset.ObjectRootServingArtifact]
	for _, statement := range []string{
		`CREATE SCHEMA ducklake_catalog`,
		`CREATE TABLE ducklake_catalog.ducklake_metadata(key text PRIMARY KEY,value text NOT NULL)`,
		`CREATE TABLE ducklake_catalog.ducklake_snapshot(snapshot_id bigint PRIMARY KEY,catalog_version bigint NOT NULL,committed_at timestamptz NOT NULL)`,
		`CREATE TABLE ducklake_catalog.ducklake_data_file(snapshot_id bigint NOT NULL,object_uri text NOT NULL,object_version text NOT NULL,object_digest text NOT NULL,PRIMARY KEY(snapshot_id,object_uri))`,
		`INSERT INTO ducklake_catalog.ducklake_metadata VALUES ('catalog_id','catalog-a'),('catalog_uuid','0198f2c0-7c7a-7f00-8a11-000000009981'),('format','ducklake:v1')`,
		`INSERT INTO ducklake_catalog.ducklake_snapshot VALUES (42,9,'2026-09-21T12:00:00Z')`,
	} {
		if _, err := duck.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := duck.Exec(t.Context(), `INSERT INTO ducklake_catalog.ducklake_data_file VALUES (42,$1,$2,$3),(42,$4,$5,$6)`, duckRoot.uri, duckRoot.version, duckRoot.digest, artifactRoot.uri, artifactRoot.version, artifactRoot.digest); err != nil {
		t.Fatal(err)
	}

	runContainerCommand(t, fixture.container, "su", "postgres", "-c", "pg_basebackup -U postgres -h /var/run/postgresql -D /tmp/fai981-physical-base -Fp -X stream -c fast")
	backupLabelDigest := strings.TrimSpace(runContainerCommand(t, fixture.container, "sha256sum", "/tmp/fai981-physical-base/backup_label"))
	backupLabelDigest, _, _ = strings.Cut(backupLabelDigest, " ")
	fixture.backupIdentity = fmt.Sprintf("pg-basebackup:sha256:%s:timeline:%s", backupLabelDigest, fixture.timeline)
	backups := map[recoveryset.DatabaseRole]qualificationBackup{
		recoveryset.DatabaseControl: {
			stateDigest: qualificationDatabaseStateDigest(t, control, recoveryset.DatabaseControl),
			point:       recoveryset.ClusterRecoveryPoint{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: fixture.clusterIdentity, DatabaseIdentity: "fai981_control_source", RecoveryIdentity: fixture.backupIdentity},
		},
		recoveryset.DatabaseDuckLake: {
			stateDigest: qualificationDatabaseStateDigest(t, duck, recoveryset.DatabaseDuckLake),
			point:       recoveryset.ClusterRecoveryPoint{DatabaseRole: recoveryset.DatabaseDuckLake, ClusterIdentity: fixture.clusterIdentity, DatabaseIdentity: "fai981_ducklake_source", RecoveryIdentity: fixture.backupIdentity},
		},
	}
	if _, err := control.Exec(t.Context(), `UPDATE application_state SET state_value='corrupted-after-backup'`); err != nil {
		t.Fatal(err)
	}
	if _, err := duck.Exec(t.Context(), `DELETE FROM ducklake_catalog.ducklake_data_file`); err != nil {
		t.Fatal(err)
	}
	return backups
}

func (fixture *qualificationPostgresFixture) createAuthorityDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	fixture.createDatabase(t, "fai981_recovery_authority")
	pool := openQualificationPool(t, fixture.databaseURL(t, "fai981_recovery_authority"))
	if err := postgresmigrations.ApplyRiver(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := recoverypg.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := jobspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := refreshpg.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func (fixture *qualificationPostgresFixture) createDatabase(t *testing.T, name string) {
	t.Helper()
	if _, err := fixture.admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
}

func (fixture *qualificationPostgresFixture) databaseURL(t *testing.T, name string) string {
	t.Helper()
	value, err := fixture.container.ConnectionString(t.Context(), "sslmode=disable", "dbname="+name)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type qualificationDatabaseProvider struct {
	fixture      *qualificationPostgresFixture
	backups      map[recoveryset.DatabaseRole]qualificationBackup
	mu           sync.Mutex
	restoredURLs map[recoveryset.DatabaseRole]string
}

func (provider *qualificationDatabaseProvider) RestoreCluster(ctx context.Context, request providerrestore.DatabaseRequest) ([]providerrestore.DatabaseResult, error) {
	if len(request.Points) == 0 {
		return nil, fmt.Errorf("PostgreSQL recovery points are required")
	}
	for _, point := range request.Points {
		backup, ok := provider.backups[point.DatabaseRole]
		if !ok || backup.point != point || point.ClusterIdentity != request.Points[0].ClusterIdentity || point.RecoveryIdentity != request.Points[0].RecoveryIdentity {
			return nil, fmt.Errorf("unknown PostgreSQL recovery point")
		}
	}
	started := time.Now().UTC()
	if err := runContainerCommandError(ctx, provider.fixture.container, "su", "postgres", "-c", "pg_ctl -D /tmp/fai981-physical-base status"); err != nil {
		if err := runContainerCommandError(ctx, provider.fixture.container, "su", "postgres", "-c", `pg_ctl -D /tmp/fai981-physical-base -o "-p 55432 -k /tmp -c listen_addresses='*' -c ssl=on -c ssl_cert_file=/etc/fai981-tls/server.crt -c ssl_key_file=/etc/fai981-tls/server.key -c ssl_ca_file=/etc/fai981-tls/ca.crt" -w start`); err != nil {
			return nil, err
		}
	}
	completed := time.Now().UTC()
	results := make([]providerrestore.DatabaseResult, 0, len(request.Points))
	for _, point := range request.Points {
		backup := provider.backups[point.DatabaseRole]
		restoredURL, err := provider.fixture.restoredDatabaseURL(ctx, point.DatabaseIdentity)
		if err != nil {
			return nil, err
		}
		pool, err := pgxpool.New(ctx, restoredURL)
		if err != nil {
			return nil, err
		}
		stateDigest, err := qualificationDatabaseStateDigestContext(ctx, pool, point.DatabaseRole)
		pool.Close()
		if err != nil || stateDigest != backup.stateDigest {
			return nil, fmt.Errorf("restored PostgreSQL state mismatch: %w", err)
		}
		provider.mu.Lock()
		provider.restoredURLs[point.DatabaseRole] = restoredURL
		provider.mu.Unlock()
		result := providerrestore.DatabaseResult{
			Provider: "postgresql-physical-basebackup", OperationID: "postgres-physical-restore:" + digestText(point.RecoveryIdentity),
			DatabaseRole: point.DatabaseRole, ClusterIdentity: point.ClusterIdentity,
			DatabaseIdentity: point.DatabaseIdentity, RecoveryIdentity: point.RecoveryIdentity,
			StateDigest: stateDigest, StartedAt: started, CompletedAt: completed,
		}
		if point.DatabaseRole == recoveryset.DatabaseDuckLake {
			catalog := request.Catalog
			result.Catalog = &catalog
		}
		results = append(results, result)
	}
	return results, nil
}

func (fixture *qualificationPostgresFixture) restoredDatabaseURL(ctx context.Context, database string) (string, error) {
	host, err := fixture.container.Host(ctx)
	if err != nil {
		return "", err
	}
	port, err := fixture.container.MappedPort(ctx, "55432/tcp")
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "postgres", User: url.UserPassword("postgres", fixture.password), Host: net.JoinHostPort(host, port.Port()), Path: "/" + database, RawQuery: "sslmode=disable"}).String(), nil
}

func (fixture *qualificationPostgresFixture) consumerDatabaseURL(database string) string {
	port, err := fixture.container.MappedPort(context.Background(), "55432/tcp")
	if err != nil {
		panic(err)
	}
	return (&url.URL{Scheme: "postgres", User: url.UserPassword("postgres", fixture.password), Host: net.JoinHostPort(fixture.providerHost, port.Port()), Path: "/" + database, RawQuery: "sslmode=verify-full"}).String()
}

type qualificationObjectPoint struct {
	uri, key, version, digest string
	damageVersions            []string
}

type qualificationObjects struct {
	client                 *awss3.Client
	bucket                 string
	endpoint, user, secret string
	resourceID             string
	rootCA                 string
}

func startQualificationObjects(t *testing.T, networkName string, manifest providerrestore.RetainedResourceManifestStore, runID, providerHost string, material qualificationTLSMaterial) *qualificationObjects {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	user, secret := "fai981"+strings.ReplaceAll(uuid.NewString(), "-", ""), uuid.NewString()
	containerName := "leapview-fai981-minio-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	objectPort := freeLoopbackPort(t)
	container, err := tcminio.Run(ctx, qualificationMinIOImage, tcminio.WithUsername(user), tcminio.WithPassword(secret),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{Reader: strings.NewReader(material.serverCert), ContainerFilePath: "/root/.minio/certs/public.crt", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader(material.serverKey), ContainerFilePath: "/root/.minio/certs/private.key", FileMode: 0o600},
		),
		testcontainers.WithReuseByName(containerName),
		tcnetwork.WithNetworkName([]string{"fai981-objects"}, networkName),
		tcnetwork.WithBridgeNetwork(),
		testcontainers.WithAdditionalLifecycleHooks(persistResourceAfterCreate(manifest, runID)),
		testcontainers.WithHostConfigModifier(func(config *dockercontainer.HostConfig) {
			config.PortBindings = dockernetwork.PortMap{
				dockernetwork.MustParsePort("9000/tcp"): {{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: objectPort}},
			}
		}),
		testcontainers.WithTmpfs(map[string]string{"/data": "rw,size=1g"}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/minio/health/ready").WithPort("9000").WithTLS(true).WithAllowInsecure(true).WithStartupTimeout(time.Minute)))
	if err != nil {
		t.Fatalf("start required versioned object provider: %v", err)
	}
	if err := manifest.Record(runID, providerrestore.RetainedResource{Kind: "container", ID: container.GetContainerID()}); err != nil {
		_ = testcontainers.TerminateContainer(container)
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := (&url.URL{Scheme: "https", Host: net.JoinHostPort(providerHost, port.Port())}).String()
	client := awss3.New(awss3.Options{Region: "us-east-1", BaseEndpoint: aws.String(endpoint), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(user, secret, ""), RetryMaxAttempts: 1, HTTPClient: qualificationTLSHTTPClient(t, material.caCert)})
	bucket := "fai981-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: &bucket}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PutBucketVersioning(ctx, &awss3.PutBucketVersioningInput{Bucket: &bucket, VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled}}); err != nil {
		t.Fatal(err)
	}
	return &qualificationObjects{client: client, bucket: bucket, endpoint: endpoint, user: user, secret: secret, resourceID: container.GetContainerID(), rootCA: material.caCert}
}

func (objects *qualificationObjects) seedAndDamage(t *testing.T) map[string]qualificationObjectPoint {
	t.Helper()
	points := map[string]qualificationObjectPoint{}
	for kind, key := range map[string]string{recoveryset.ObjectRootDuckLake: "ducklake/snapshot-42/root.json", recoveryset.ObjectRootServingArtifact: "serving/generation/root.json"} {
		body := []byte(fmt.Sprintf(`{"kind":%q,"recoveryPoint":"fai981","snapshot":42}`, kind))
		version := objects.put(t, key, body)
		point := qualificationObjectPoint{uri: "s3://" + objects.bucket + "/" + key, key: key, version: version, digest: digestBytes(body)}
		corruptVersion := objects.put(t, key, []byte("corrupted-after-recovery-point"))
		deleted, err := objects.client.DeleteObject(t.Context(), &awss3.DeleteObjectInput{Bucket: &objects.bucket, Key: &key})
		if err != nil {
			t.Fatal(err)
		}
		deleteVersion := aws.ToString(deleted.VersionId)
		if !aws.ToBool(deleted.DeleteMarker) || deleteVersion == "" {
			t.Fatal("versioned object provider did not create a delete marker")
		}
		point.damageVersions = []string{deleteVersion, corruptVersion}
		points[kind] = point
	}
	return points
}

func (objects *qualificationObjects) put(t *testing.T, key string, body []byte) string {
	t.Helper()
	out, err := objects.client.PutObject(t.Context(), &awss3.PutObjectInput{Bucket: &objects.bucket, Key: &key, Body: bytes.NewReader(body)})
	if err != nil {
		t.Fatal(err)
	}
	version := aws.ToString(out.VersionId)
	if version == "" || version == "null" {
		t.Fatal("object provider did not return an immutable version")
	}
	return version
}

type qualificationObjectProvider struct {
	fixture *qualificationObjects
	points  map[string]qualificationObjectPoint
	mu      sync.Mutex
	done    map[string]bool
}

func (provider *qualificationObjectProvider) RestoreObject(ctx context.Context, request providerrestore.ObjectRequest) (providerrestore.ObjectResult, error) {
	started := time.Now().UTC()
	point, ok := provider.points[request.Root.Kind]
	if !ok || point.uri != request.Root.URI || point.version != request.Root.VersionID || point.digest != request.Root.Digest {
		return providerrestore.ObjectResult{}, fmt.Errorf("unknown object recovery point")
	}
	provider.mu.Lock()
	if provider.done == nil {
		provider.done = map[string]bool{}
	}
	alreadyDone := provider.done[request.IdempotencyKey]
	provider.mu.Unlock()
	if !alreadyDone {
		for _, version := range point.damageVersions {
			if _, err := provider.fixture.client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: &provider.fixture.bucket, Key: &point.key, VersionId: &version}); err != nil {
				return providerrestore.ObjectResult{}, err
			}
		}
	}
	observedVersion, observedDigest, err := provider.fixture.current(ctx, point.key)
	if err != nil {
		return providerrestore.ObjectResult{}, err
	}
	if observedVersion != point.version || observedDigest != point.digest {
		return providerrestore.ObjectResult{}, fmt.Errorf("restored object does not match exact retained version")
	}
	provider.mu.Lock()
	provider.done[request.IdempotencyKey] = true
	provider.mu.Unlock()
	return providerrestore.ObjectResult{Provider: "minio-s3-versioning", OperationID: "object-restore:" + request.IdempotencyKey, Kind: request.Root.Kind, URI: request.Root.URI, RequiredVersionID: request.Root.VersionID, ObservedVersionID: observedVersion, Digest: observedDigest, StartedAt: started, CompletedAt: time.Now().UTC()}, nil
}

func (objects *qualificationObjects) current(ctx context.Context, key string) (string, string, error) {
	out, err := objects.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: &objects.bucket, Key: &key})
	if err != nil {
		return "", "", err
	}
	if out.Body == nil {
		return "", "", fmt.Errorf("object provider returned no body")
	}
	body, readErr := io.ReadAll(out.Body)
	closeErr := out.Body.Close()
	if readErr != nil || closeErr != nil {
		return "", "", fmt.Errorf("read restored object: %w", errorsJoin(readErr, closeErr))
	}
	return aws.ToString(out.VersionId), digestBytes(body), nil
}

type qualificationVerifier struct {
	databases *qualificationDatabaseProvider
	objects   *qualificationObjects
	points    map[string]qualificationObjectPoint
	expected  map[recoveryset.DatabaseRole]qualificationBackup
}

func (verifier *qualificationVerifier) Verify(ctx context.Context, request providerrestore.VerificationRequest) (providerrestore.VerificationResult, error) {
	digests := map[recoveryset.DatabaseRole]string{}
	for _, role := range []recoveryset.DatabaseRole{recoveryset.DatabaseControl, recoveryset.DatabaseDuckLake} {
		verifier.databases.mu.Lock()
		databaseURL := verifier.databases.restoredURLs[role]
		verifier.databases.mu.Unlock()
		pool, err := pgxpool.New(ctx, databaseURL)
		if err != nil {
			return providerrestore.VerificationResult{}, err
		}
		digest, err := qualificationDatabaseStateDigestContext(ctx, pool, role)
		pool.Close()
		if err != nil || digest != verifier.expected[role].stateDigest {
			return providerrestore.VerificationResult{}, fmt.Errorf("post-restore database verification failed: %w", err)
		}
		digests[role] = digest
	}
	for _, point := range verifier.points {
		version, digest, err := verifier.objects.current(ctx, point.key)
		if err != nil || version != point.version || digest != point.digest {
			return providerrestore.VerificationResult{}, fmt.Errorf("post-restore object verification failed: %w", err)
		}
	}
	return providerrestore.VerificationResult{ProviderOperationID: "coordinated-verification:" + request.Set.FrontierDigest, ControlStateDigest: digests[recoveryset.DatabaseControl], DuckLakeStateDigest: digests[recoveryset.DatabaseDuckLake], Catalog: request.Set.Catalog, ObjectsConsistent: true, Ready: true, VerifiedAt: time.Now().UTC()}, nil
}

func qualificationRecoverySet(t *testing.T, cluster string, backups map[recoveryset.DatabaseRole]qualificationBackup, objects *qualificationObjects, points map[string]qualificationObjectPoint) recoveryset.RecoverySet {
	t.Helper()
	compatibility := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	compatibilityDigest, err := compatibility.Digest()
	if err != nil {
		t.Fatal(err)
	}
	duck, artifact := points[recoveryset.ObjectRootDuckLake], points[recoveryset.ObjectRootServingArtifact]
	set := recoveryset.RecoverySet{
		ID: uuid.NewString(), SchemaVersion: recoveryset.SchemaVersion,
		ClusterPoints: []recoveryset.ClusterRecoveryPoint{backups[recoveryset.DatabaseControl].point, backups[recoveryset.DatabaseDuckLake].point},
		Delivery:      recoveryset.DeliveryPointer{TargetID: "target-fai981-isolated", GenerationID: uuid.NewString(), PublicationID: uuid.NewString(), TargetRevision: 42},
		Serving:       recoveryset.SnapshotSeal{SealID: uuid.NewString(), PhysicalPoolID: "pool-fai981", TenantDomain: "tenant-a", Region: "isolated-provider", EncryptionDomain: "qualification-key-reference", ObjectNamespace: objects.bucket, CatalogDatabase: backups[recoveryset.DatabaseDuckLake].point.DatabaseIdentity, CatalogID: "catalog-a", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000009981", CatalogVersion: 9, DuckLakeSnapshotID: 42, RelationManifestDigest: digestText("relation-manifest"), RelationNamespace: "snapshot/42", ClosureDigest: digestText("closure"), ObjectRoot: duck.uri, ObjectRootDigest: duck.digest, ArtifactRoot: artifact.uri, ArtifactRootDigest: artifact.digest, ServingArtifactID: "artifact-fai981", ServingArtifactDigest: digestText("serving-artifact"), CompiledGraphDigest: digestText("graph"), CompiledConfigDigest: digestText("config"), SecurityDomainFingerprint: digestText("security"), RequestDigest: digestText("request"), PlanDigest: digestText("plan"), CompatibilityDigest: compatibilityDigest, DuckDBVersion: "1", RuntimeVersion: "1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1"},
		Catalog:       recoveryset.CatalogCommit{CatalogID: "catalog-a", CatalogDatabase: backups[recoveryset.DatabaseDuckLake].point.DatabaseIdentity, CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000009981", CatalogVersion: 9, SnapshotID: 42},
		ObjectRoots:   []recoveryset.ObjectRoot{{Kind: recoveryset.ObjectRootDuckLake, URI: duck.uri, VersionID: duck.version, Digest: duck.digest, ProviderRecoveryFrontier: "minio-version:" + duck.version}, {Kind: recoveryset.ObjectRootServingArtifact, URI: artifact.uri, VersionID: artifact.version, Digest: artifact.digest, ProviderRecoveryFrontier: "minio-version:" + artifact.version}},
		Compatibility: compatibility, FenceEpoch: 1, AuditIdentity: "fai981-qualification", Status: recoveryset.StatusPrepared, CreatedBy: "fai981-qualification", CreatedAt: time.Now().UTC(),
	}
	normalized, err := set.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func qualificationOccurrence(t *testing.T, ledger *refreshpg.RecoveryLedger, set recoveryset.RecoverySet, artifactIdentity string) recovery.Occurrence {
	t.Helper()
	now := time.Now().UTC()
	definition := recovery.Definition{ScheduleID: "fai981-provider-restore", Scenario: "coordinated-provider-restore", Operation: recovery.OperationRestore, PolicyVersion: "ubdr-provider-restore-v1", PolicySHA256: strings.Repeat("a", 64), TargetScope: set.Delivery.TargetID, ArtifactIdentity: artifactIdentity, Cron: "@daily", Timezone: "UTC", StaleAfter: 24 * time.Hour, Enabled: true}
	if err := ledger.ReconcileSchedule(t.Context(), definition, now); err != nil {
		t.Fatal(err)
	}
	revision, err := recovery.ScheduleRevisionID(definition)
	if err != nil {
		t.Fatal(err)
	}
	input := recovery.EnqueueInput{ScheduleID: definition.ScheduleID, ScheduleRevision: revision, Scenario: definition.Scenario, Operation: definition.Operation, PolicyVersion: definition.PolicyVersion, PolicySHA256: definition.PolicySHA256, TargetScope: definition.TargetScope, ArtifactIdentity: definition.ArtifactIdentity, PlannedAt: now.Add(-5 * time.Minute), StaleAfter: definition.StaleAfter}
	enqueued, created, err := ledger.Enqueue(t.Context(), input, now)
	if err != nil || !created {
		t.Fatalf("enqueue provider restore occurrence: created=%v err=%v", created, err)
	}
	claimed, ok, err := ledger.ClaimNext(t.Context(), recovery.ClaimInput{WorkerID: "fai981-provider-worker", Actor: "fai981-qualification", Now: time.Now().UTC(), Lease: 20 * time.Minute})
	if err != nil || !ok || claimed.ID != enqueued.ID {
		t.Fatalf("claim provider restore occurrence: occurrence=%q ok=%v err=%v", claimed.ID, ok, err)
	}
	if err := ledger.Start(t.Context(), claimed.ID, claimed.Fence, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	started, err := ledger.Occurrence(t.Context(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	return started
}

func qualificationDatabaseStateDigest(t *testing.T, pool *pgxpool.Pool, role recoveryset.DatabaseRole) string {
	t.Helper()
	digest, err := qualificationDatabaseStateDigestContext(t.Context(), pool, role)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func qualificationDatabaseStateDigestContext(ctx context.Context, pool *pgxpool.Pool, role recoveryset.DatabaseRole) (string, error) {
	query := `SELECT id::text,state_key,state_value FROM application_state ORDER BY id`
	if role == recoveryset.DatabaseDuckLake {
		query = `
SELECT 'metadata',key,value FROM ducklake_catalog.ducklake_metadata
UNION ALL SELECT 'snapshot',snapshot_id::text,catalog_version::text FROM ducklake_catalog.ducklake_snapshot
UNION ALL SELECT 'object',object_uri,object_version||':'||object_digest FROM ducklake_catalog.ducklake_data_file
ORDER BY 1,2,3`
	}
	rows, err := pool.Query(ctx, query)
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

func assertQualificationResult(t *testing.T, report providerrestore.Report, occurrence recovery.Occurrence, set recoveryset.RecoverySet, objects map[string]qualificationObjectPoint) {
	t.Helper()
	if report.Status != providerrestore.StatusSucceeded || occurrence.Status != recovery.StatusSucceeded || set.Status != recoveryset.StatusPublished {
		t.Fatalf("coordinated result incomplete: report=%q occurrence=%q set=%q", report.Status, occurrence.Status, set.Status)
	}
	if len(report.Databases) != 2 || len(report.Objects) != 2 || !report.Verification.Ready || !report.Verification.ObjectsConsistent {
		t.Fatalf("coordinated provider evidence incomplete: %#v", report)
	}
	if report.Handoff.Status != providerrestore.HandoffAvailable || report.Handoff.Artifact.Image != occurrence.ArtifactIdentity || len(report.Handoff.Providers) != 3 || len(report.Handoff.Secrets.Keys) != 5 || strings.HasPrefix(report.Handoff.Secrets.URI, "file://") {
		t.Fatalf("replacement-host handoff incomplete: %#v", report.Handoff)
	}
	for _, endpoint := range report.Handoff.Providers {
		if strings.Contains(endpoint.Endpoint, "fai981-") || strings.Contains(endpoint.Endpoint, "127.0.0.1") || (endpoint.Role == "objects" && !strings.HasPrefix(endpoint.Endpoint, "https://")) || (endpoint.Role != "objects" && !strings.Contains(endpoint.Endpoint, "sslmode=verify-full")) {
			t.Fatalf("replacement-host handoff contains a producer-only or insecure endpoint: %#v", endpoint)
		}
	}
	if occurrence.RestoreStartedAt.IsZero() || occurrence.RestoreCompletedAt.IsZero() || occurrence.RestoreCompletedAt.Before(occurrence.RestoreStartedAt) || len(occurrence.Evidence) != 1 {
		t.Fatalf("durable occurrence phase/evidence incomplete: %#v", occurrence)
	}
	for _, result := range report.Objects {
		point := objects[result.Kind]
		if result.ObservedVersionID != point.version || result.Digest != point.digest {
			t.Fatalf("object result is not exact retained version: %#v", result)
		}
	}
}

func writeQualificationEvidence(t *testing.T, root string, value any, secrets ...string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("credential-bearing data reached qualification evidence")
		}
	}
	path := filepath.Join(root, "coordinated-provider-restore.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	assertQualificationEvidenceCredentialFree(t, root, secrets...)
}

func assertQualificationEvidenceCredentialFree(t *testing.T, root string, secrets ...string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(bytes.ToLower(raw), []byte("password")) {
			return fmt.Errorf("credential-bearing content in %s", path)
		}
		for _, secret := range secrets {
			if secret != "" && bytes.Contains(raw, []byte(secret)) {
				return fmt.Errorf("credential-bearing content in %s", path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func openQualificationPool(t *testing.T, databaseURL string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func openQualificationTLSPool(t *testing.T, databaseURL, rootCA string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(rootCA)) || config.ConnConfig.TLSConfig == nil {
		t.Fatal("qualification PostgreSQL TLS configuration is incomplete")
	}
	config.ConnConfig.TLSConfig.RootCAs = roots
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func runContainerCommand(t *testing.T, container *tcpostgres.PostgresContainer, command ...string) string {
	t.Helper()
	output, err := runContainerCommandOutput(t.Context(), container, command...)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func runContainerCommandError(ctx context.Context, container *tcpostgres.PostgresContainer, command ...string) error {
	_, err := runContainerCommandOutput(ctx, container, command...)
	return err
}

func runContainerCommandOutput(ctx context.Context, container *tcpostgres.PostgresContainer, command ...string) (string, error) {
	exitCode, reader, err := container.Exec(ctx, command, tcexec.Multiplexed())
	if err != nil {
		return "", err
	}
	output, readErr := io.ReadAll(reader)
	if readErr != nil {
		return "", readErr
	}
	if exitCode != 0 {
		return "", fmt.Errorf("provider command %q exited %d: %s", command[0], exitCode, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestText(value string) string { return digestBytes([]byte(value)) }

func errorsJoin(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func parseObjectURI(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "s3" || parsed.Host == "" || strings.TrimPrefix(parsed.Path, "/") == "" {
		return nil, fmt.Errorf("invalid object URI")
	}
	return parsed, nil
}
