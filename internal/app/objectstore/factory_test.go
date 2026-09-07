package objectstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/app/config"
	platformstore "github.com/flidai/leapview/internal/platform/objectstore"
)

func TestFilesystemFactoryDerivesStableDomainAndPrivateRoot(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{HomeDir: home, ObjectStoreBackend: "filesystem"}
	store, domain, err := New(context.Background(), cfg, "instance-a", "production")
	if err != nil {
		t.Fatal(err)
	}
	if domain == "" || len(domain) != len("sha256:")+64 {
		t.Fatalf("domain = %q", domain)
	}
	_, domainAgain, err := New(context.Background(), cfg, "instance-a", "production")
	if err != nil || domainAgain != domain {
		t.Fatalf("domain changed across construction: %q / %q (err=%v)", domain, domainAgain, err)
	}
	if _, ok := store.(*platformstore.FilesystemStore); !ok {
		t.Fatalf("store type = %T, want filesystem", store)
	}
	if _, err := filepath.Abs(filepath.Join(home, "artifacts", "object-store")); err != nil {
		t.Fatal(err)
	}
	if changed := func() string {
		_, d, _ := New(context.Background(), cfg, "instance-b", "production")
		return d
	}(); changed == domain {
		t.Fatal("instance identity did not change security domain")
	}
	if changed := func() string {
		_, d, _ := New(context.Background(), cfg, "instance-a", "staging")
		return d
	}(); changed == domain {
		t.Fatal("environment did not change security domain")
	}
	if _, _, err := New(context.Background(), config.Config{HomeDir: home, ObjectStoreBackend: "filesystem", ObjectStoreFilesystemRoot: home}, "instance-a", "production"); err == nil {
		t.Fatal("broad home directory was accepted as object-store root")
	}
	info, err := os.Stat(filepath.Join(home, "artifacts", "object-store"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("object-store root mode = %o, want 700", info.Mode().Perm())
	}
}

func TestS3FactoryUsesInjectedConstructorsAndAmbientCredentials(t *testing.T) {
	var gotLoadOptions int
	var gotClientOptions int
	var gotRegion string
	var gotCredentials bool
	var gotEndpoint string
	var gotPathStyle bool
	fakeClient := &stubS3Client{}
	cfg := config.Config{ObjectStoreBackend: "s3", ObjectStoreS3Bucket: "objects", ObjectStoreS3Region: "eu-west-1", ObjectStoreS3Prefix: "project", ObjectStoreS3Endpoint: "https://S3.example.com/", ObjectStoreS3EncryptionMode: "AES256", ObjectStoreS3PathStyle: true}
	_, domain, err := NewWithOptions(context.Background(), cfg, "instance-a", "production", Options{
		LoadAWSConfig: func(_ context.Context, options ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
			gotLoadOptions = len(options)
			loadOptions := awsconfig.LoadOptions{}
			for _, option := range options {
				if err := option(&loadOptions); err != nil {
					return aws.Config{}, err
				}
			}
			gotRegion, gotCredentials = loadOptions.Region, loadOptions.Credentials != nil
			return aws.Config{Region: "eu-west-1"}, nil
		},
		NewS3Client: func(_ aws.Config, options ...func(*awss3.Options)) platformstore.S3Client {
			gotClientOptions = len(options)
			clientOptions := awss3.Options{}
			for _, option := range options {
				option(&clientOptions)
			}
			gotEndpoint, gotPathStyle = aws.ToString(clientOptions.BaseEndpoint), clientOptions.UsePathStyle
			return fakeClient
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if domain == "" || gotLoadOptions != 1 || gotClientOptions != 1 || gotRegion != "eu-west-1" || gotCredentials || gotEndpoint != "https://s3.example.com" || !gotPathStyle {
		t.Fatalf("domain=%q loadOptions=%d clientOptions=%d region=%q credentials=%v endpoint=%q pathStyle=%v", domain, gotLoadOptions, gotClientOptions, gotRegion, gotCredentials, gotEndpoint, gotPathStyle)
	}
	static := cfg
	static.ObjectStoreS3AccessKeyID, static.ObjectStoreS3SecretAccessKey, static.ObjectStoreS3SessionToken = "access", "secret", "session"
	if _, _, err := NewWithOptions(context.Background(), static, "instance-a", "production", Options{
		LoadAWSConfig: func(_ context.Context, options ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
			loadOptions := awsconfig.LoadOptions{}
			for _, option := range options {
				if err := option(&loadOptions); err != nil {
					return aws.Config{}, err
				}
			}
			if loadOptions.Credentials == nil {
				t.Fatal("static credentials were not configured")
			}
			return aws.Config{}, nil
		},
		NewS3Client: func(aws.Config, ...func(*awss3.Options)) platformstore.S3Client { return fakeClient },
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewWithOptions(context.Background(), config.Config{ObjectStoreBackend: "s3", ObjectStoreS3Bucket: "objects", ObjectStoreS3Region: "eu-west-1", ObjectStoreS3AccessKeyID: "only"}, "instance-a", "production", Options{LoadAWSConfig: func(context.Context, ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
		return aws.Config{}, nil
	}, NewS3Client: func(aws.Config, ...func(*awss3.Options)) platformstore.S3Client { return fakeClient }}); err == nil {
		t.Fatal("partial static credentials were accepted")
	}
	kms := cfg
	kms.ObjectStoreS3EncryptionMode = "aws:kms"
	kms.ObjectStoreS3EncryptionKeyRef = "logical-ref"
	kms.ObjectStoreS3EncryptionProviderKey = "arn:aws:kms:eu-west-1:123456789012:key/real"
	_, kmsDomain, err := NewWithOptions(context.Background(), kms, "instance-a", "production", Options{
		LoadAWSConfig: func(context.Context, ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
			return aws.Config{}, nil
		},
		NewS3Client: func(aws.Config, ...func(*awss3.Options)) platformstore.S3Client { return fakeClient },
	})
	if err != nil {
		t.Fatalf("valid SSE-KMS object store rejected: %v", err)
	}
	if kmsDomain == domain {
		t.Fatal("SSE-KMS encryption identity did not change storage domain")
	}
}

func TestS3FactorySSECustomerConfigurationAndDomainIdentity(t *testing.T) {
	customerKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	base := config.Config{
		ObjectStoreBackend:                 "s3",
		ObjectStoreS3Bucket:                "objects",
		ObjectStoreS3Region:                "eu-west-1",
		ObjectStoreS3Endpoint:              "https://s3.example.com",
		ObjectStoreS3EncryptionMode:        "sse-c",
		ObjectStoreS3EncryptionKeyRef:      "customer-epoch-1",
		ObjectStoreS3EncryptionCustomerKey: customerKey,
	}
	options := Options{
		LoadAWSConfig: func(context.Context, ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
			return aws.Config{}, nil
		},
		NewS3Client: func(aws.Config, ...func(*awss3.Options)) platformstore.S3Client { return &stubS3Client{} },
	}
	if _, domain, err := NewWithOptions(context.Background(), base, "instance-a", "production", options); err != nil || domain == "" {
		t.Fatalf("valid SSE-C object store = %q, %v", domain, err)
	}
	_, domain, err := NewWithOptions(context.Background(), base, "instance-a", "production", options)
	if err != nil {
		t.Fatal(err)
	}
	changedCustomerKey := base
	changedCustomerKey.ObjectStoreS3EncryptionCustomerKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	_, changedDomain, err := NewWithOptions(context.Background(), changedCustomerKey, "instance-a", "production", options)
	if err != nil {
		t.Fatal(err)
	}
	if changedDomain != domain {
		t.Fatalf("customer key changed security domain: %q != %q", changedDomain, domain)
	}
	changedEpoch := base
	changedEpoch.ObjectStoreS3EncryptionKeyRef = "customer-epoch-2"
	_, epochDomain, err := NewWithOptions(context.Background(), changedEpoch, "instance-a", "production", options)
	if err != nil {
		t.Fatal(err)
	}
	if epochDomain == domain {
		t.Fatal("opaque customer-key epoch did not change security domain")
	}
	for name, endpoint := range map[string]string{"http": "http://s3.example.com"} {
		t.Run(name, func(t *testing.T) {
			invalid := base
			invalid.ObjectStoreS3Endpoint = endpoint
			if _, _, err := NewWithOptions(context.Background(), invalid, "instance-a", "production", options); err == nil {
				t.Fatalf("SSE-C endpoint %q was accepted", endpoint)
			}
		})
	}
	withoutCustomEndpoint := base
	withoutCustomEndpoint.ObjectStoreS3Endpoint = ""
	if _, _, err := NewWithOptions(context.Background(), withoutCustomEndpoint, "instance-a", "production", options); err != nil {
		t.Fatalf("SSE-C with AWS default endpoint rejected: %v", err)
	}
}

func TestDeriveStorageSecurityDomainChangesWithProviderIdentity(t *testing.T) {
	a, err := DeriveStorageSecurityDomain("instance", "production", "s3:bucket|prefix=objects", "AES256")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeriveStorageSecurityDomain("instance", "production", "s3:bucket|prefix=objects", "aws:kms|opaque=ref|provider=arn")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("encryption identity did not affect domain")
	}
}

func TestFactoryRejectsNonCanonicalIdentityInputs(t *testing.T) {
	for name, test := range map[string]struct {
		cfg         config.Config
		instance    string
		environment string
	}{
		"backend whitespace":     {cfg: config.Config{ObjectStoreBackend: " filesystem"}, instance: "instance", environment: "production"},
		"instance whitespace":    {cfg: config.Config{ObjectStoreBackend: "filesystem"}, instance: " instance", environment: "production"},
		"environment whitespace": {cfg: config.Config{ObjectStoreBackend: "filesystem"}, instance: "instance", environment: "production "},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := New(context.Background(), test.cfg, test.instance, test.environment); err == nil {
				t.Fatal("noncanonical identity input was accepted")
			}
		})
	}
}

func TestS3FactoryRejectsInvalidEncryptionWithoutFilesystemFallback(t *testing.T) {
	called := false
	base := config.Config{HomeDir: t.TempDir(), ObjectStoreBackend: "s3", ObjectStoreS3Bucket: "objects", ObjectStoreS3Region: "eu-west-1", ObjectStoreS3EncryptionMode: "aws:kms", ObjectStoreS3EncryptionKeyRef: "logical"}
	_, _, err := NewWithOptions(context.Background(), base, "instance", "production", Options{
		LoadAWSConfig: func(context.Context, ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
			return aws.Config{}, nil
		},
		NewS3Client: func(aws.Config, ...func(*awss3.Options)) platformstore.S3Client {
			called = true
			return &stubS3Client{}
		},
	})
	if err == nil || called {
		t.Fatalf("unresolved KMS identity was accepted (err=%v clientCalled=%v)", err, called)
	}
	base.ObjectStoreS3EncryptionMode = "sse-kms"
	if err := func() error { _, _, err := New(context.Background(), base, "instance", "production"); return err }(); err == nil {
		t.Fatal("noncanonical encryption mode was accepted")
	}
}

type stubS3Client struct{}

func (stubS3Client) PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	return nil, errors.New("stub")
}
func (stubS3Client) HeadObject(context.Context, *awss3.HeadObjectInput, ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	return nil, errors.New("stub")
}
func (stubS3Client) GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	return nil, errors.New("stub")
}
func (stubS3Client) DeleteObject(context.Context, *awss3.DeleteObjectInput, ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error) {
	return nil, errors.New("stub")
}
func (stubS3Client) ListObjectsV2(context.Context, *awss3.ListObjectsV2Input, ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	return nil, io.EOF
}
