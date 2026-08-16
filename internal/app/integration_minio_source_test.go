package app

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/workload"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/log"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
)

const (
	minIOIntegrationImage  = "minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"
	minIOIntegrationUser   = "leapview"
	minIOIntegrationSecret = "leapview-integration-secret"
)

func TestMinIOParquetSourceRefreshContract(t *testing.T) {
	ctx := context.Background()
	endpoint := startMinIO(t, ctx)
	const (
		bucket = "leapview-integration"
		key    = "orders/current.parquet"
		region = "us-east-1"
	)
	client := minIOClient(t, ctx, endpoint, region, minIOIntegrationUser, minIOIntegrationSecret)
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create MinIO bucket: %v", err)
	}

	putMinIOObject(t, ctx, client, bucket, "commerce/"+key, parquetFixture(t, 10, 20))
	credentialJSON := fmt.Sprintf(`{"access_key_id":%q,"secret_access_key":%q,"region":%q,"endpoint":%q,"url_style":"path","use_ssl":false}`,
		minIOIntegrationUser, minIOIntegrationSecret, region, strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://"))
	t.Setenv("LEAPVIEW_TEST_MINIO_CREDENTIALS", credentialJSON)
	model := minIOModel(bucket, key)
	if err := model.Validate(); err != nil {
		t.Fatalf("validate scoped MinIO model: %v", err)
	}

	escape := minIOModel(bucket, "../outside/orders.parquet")
	if err := escape.Validate(); err == nil || !strings.Contains(err.Error(), "escapes connection scope") {
		t.Fatalf("path escape validation error = %v", err)
	}

	selection, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID:    "minio-integration",
		ProjectID:   "project:commerce",
		Environment: "test",
		TargetClass: connectionbinding.TargetDevelopment,
		Kind:        connectionbinding.ResolverEnvironment,
	})
	if err != nil {
		t.Fatalf("select development credential resolver: %v", err)
	}
	credentialResolver, err := analyticsduckdb.NewDevelopmentEnvironmentCredentialResolver(selection)
	if err != nil {
		t.Fatalf("configure development credential resolver: %v", err)
	}
	db, err := analyticsducklake.Open(ctx, analyticsducklake.Config{RootDir: filepath.Join(t.TempDir(), "ducklake"), MaxConnections: 2})
	require.NoError(t, err)
	defer db.Close()
	controller, err := workload.New(workload.DefaultConfig())
	require.NoError(t, err)
	defer controller.Close()
	refreshLease, err := controller.Acquire(ctx, workload.Request{Class: workload.Refresh, PrincipalID: "commerce", Operation: "minio.refresh", EstimatedMemoryBytes: 1})
	require.NoError(t, err)
	runtime, err := analyticsduckdb.OpenProjectMaterializeRuntime(refreshLease.Context(), analyticsduckdb.ProjectRuntimeConfig{
		Models:             map[string]*semanticmodel.Model{"commerce": model},
		Database:           db,
		CredentialResolver: credentialResolver,
		ProjectID:          "project:commerce",
		Environment:        "test",
	})
	refreshLease.Release()
	if err != nil {
		t.Fatalf("initial MinIO refresh: %v", err)
	}
	defer runtime.Close()
	if got := materializedRevenue(t, ctx, controller, db); got != 30 {
		t.Fatalf("initial materialized revenue = %v, want 30", got)
	}

	putMinIOObject(t, ctx, client, bucket, "commerce/"+key, parquetFixture(t, 40, 50))
	if got := materializedRevenue(t, ctx, controller, db); got != 30 {
		t.Fatalf("external replacement changed served data before refresh: %v", got)
	}
	refreshLease, err = controller.Acquire(ctx, workload.Request{Class: workload.Refresh, PrincipalID: "commerce", Operation: "minio.refresh", EstimatedMemoryBytes: 1})
	require.NoError(t, err)
	err = runtime.Refresh(refreshLease.Context())
	refreshLease.Release()
	if err != nil {
		t.Fatalf("replacement MinIO refresh: %v", err)
	}
	if got := materializedRevenue(t, ctx, controller, db); got != 90 {
		t.Fatalf("refreshed materialized revenue = %v, want 90", got)
	}

	putMinIOObject(t, ctx, client, bucket, "commerce/"+key, []byte("not parquet"))
	refreshLease, err = controller.Acquire(ctx, workload.Request{Class: workload.Refresh, PrincipalID: "commerce", Operation: "minio.refresh", EstimatedMemoryBytes: 1})
	require.NoError(t, err)
	err = runtime.Refresh(refreshLease.Context())
	refreshLease.Release()
	if err == nil {
		t.Fatal("broken external object refresh succeeded")
	}
	if got := materializedRevenue(t, ctx, controller, db); got != 90 {
		t.Fatalf("failed refresh replaced prior materialization: %v", got)
	}
}

func startMinIO(t *testing.T, ctx context.Context) string {
	t.Helper()
	if os.Getenv("CI") == "" {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	minioContainer, err := tcminio.Run(
		ctx,
		minIOIntegrationImage,
		tcminio.WithUsername(minIOIntegrationUser),
		tcminio.WithPassword(minIOIntegrationSecret),
		testcontainers.WithLogger(log.TestLogger(t)),
	)
	testcontainers.CleanupContainer(t, minioContainer)
	require.NoError(t, err)
	endpoint, err := minioContainer.ConnectionString(ctx)
	require.NoError(t, err)
	return "http://" + strings.TrimRight(endpoint, "/")
}

func minIOClient(t *testing.T, ctx context.Context, endpoint, region, user, secret string) *awss3.Client {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(user, secret, "")),
	)
	require.NoError(t, err)
	return awss3.NewFromConfig(cfg, func(options *awss3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	})
}

func putMinIOObject(t *testing.T, ctx context.Context, client *awss3.Client, bucket, key string, body []byte) {
	t.Helper()
	if _, err := client.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body)}); err != nil {
		t.Fatalf("put MinIO object: %v", err)
	}
}

func parquetFixture(t *testing.T, revenues ...int) []byte {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("duckdb", ":memory:")
	require.NoError(t, err)
	values := make([]string, 0, len(revenues))
	for index, revenue := range revenues {
		values = append(values, fmt.Sprintf("('o%d', %d)", index+1, revenue))
	}
	path := filepath.Join(dir, "orders.parquet")
	_, err = db.Exec(`CREATE TABLE orders(order_id VARCHAR, revenue DOUBLE); INSERT INTO orders VALUES ` + strings.Join(values, ",") + `; COPY orders TO '` + analyticsduckdb.SQLString(path) + `' (FORMAT PARQUET)`)
	closeErr := db.Close()
	require.NoError(t, err)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return content
}

func minIOModel(bucket, key string) *semanticmodel.Model {
	scope := "s3://" + bucket + "/commerce/"
	return &semanticmodel.Model{
		Name:              "commerce",
		DefaultConnection: "lake",
		Connections: map[string]semanticmodel.Connection{
			"lake": {Kind: "s3", Scope: scope, Credentials: semanticmodel.ConnectionCredentials{Provider: "env", Secret: "LEAPVIEW_TEST_MINIO_CREDENTIALS"}},
		},
		Sources: map[string]semanticmodel.Source{
			"orders": {Connection: "lake", Path: "s3://" + bucket + "/commerce/" + key, Format: "parquet"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source: "orders", PrimaryKey: "order_id", Grain: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Expr: "order_id"}, "revenue": {Expr: "revenue", Type: "number"}},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"revenue": {Fact: "orders", Label: "Revenue", Aggregation: "sum", Input: semanticmodel.MeasureInput{Field: "orders.revenue"}, Empty: "zero"},
		},
	}
}

func materializedRevenue(t *testing.T, ctx context.Context, controller *workload.Controller, db *analyticsducklake.Environment) float64 {
	t.Helper()
	workloadLease, err := controller.Acquire(ctx, workload.Request{Class: workload.Interactive, PrincipalID: "commerce", Operation: "minio.query", EstimatedMemoryBytes: 1})
	require.NoError(t, err)
	defer workloadLease.Release()
	lease, err := db.Acquire(workloadLease.Context())
	require.NoError(t, err)
	defer lease.Release()
	session, err := db.Session(lease.Context())
	require.NoError(t, err)
	var total float64
	if err := session.QueryRowContext(lease.Context(), `SELECT SUM(revenue) FROM model.orders`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	return total
}
