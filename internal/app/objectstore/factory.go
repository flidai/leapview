// Package objectstore owns the application-wide immutable object store used
// for project sources and compiled serving artifacts. Managed-data storage and
// admitted DuckLake physical-pool stores are deliberately separate concerns.
package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	appconfig "github.com/flidai/leapview/internal/app/config"
	platformstore "github.com/flidai/leapview/internal/platform/objectstore"
)

// AWSConfigLoader is injectable so factory tests never need to resolve
// credentials, contact metadata services, or initialize a real provider.
type AWSConfigLoader func(context.Context, ...func(*awsconfig.LoadOptions) error) (aws.Config, error)

// S3ClientBuilder constructs the narrow client surface consumed by the
// platform object-store adapter. Production uses awss3.NewFromConfig.
type S3ClientBuilder func(aws.Config, ...func(*awss3.Options)) platformstore.S3Client

// Options contains constructor seams for tests. A zero Options value selects
// the AWS SDK v2 defaults and the SDK's production S3 client constructor.
type Options struct {
	LoadAWSConfig AWSConfigLoader
	NewS3Client   S3ClientBuilder
}

// New constructs the configured immutable project object store and derives its
// storage security domain from instance identity and provider identity. The
// domain is never accepted from a request or caller-owned object metadata.
func New(ctx context.Context, cfg appconfig.Config, instanceID, environment string) (platformstore.ImmutableStore, string, error) {
	return NewWithOptions(ctx, cfg, instanceID, environment, Options{})
}

// NewWithOptions is New with test-only AWS constructor injection.
func NewWithOptions(ctx context.Context, cfg appconfig.Config, instanceID, environment string, options Options) (platformstore.ImmutableStore, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(instanceID) != instanceID || strings.TrimSpace(environment) != environment {
		return nil, "", fmt.Errorf("instance ID and environment must not contain surrounding whitespace")
	}
	if instanceID == "" || environment == "" {
		return nil, "", fmt.Errorf("instance ID and environment are required")
	}
	if strings.TrimSpace(cfg.ObjectStoreBackend) != cfg.ObjectStoreBackend {
		return nil, "", fmt.Errorf("object-store backend must not contain surrounding whitespace")
	}
	backend := cfg.ObjectStoreBackend
	if backend == "" {
		backend = "filesystem"
	}
	switch backend {
	case "filesystem":
		root, err := filesystemRoot(cfg)
		if err != nil {
			return nil, "", err
		}
		namespace := "filesystem:" + root
		domain, err := DeriveStorageSecurityDomain(instanceID, environment, namespace, "filesystem")
		if err != nil {
			return nil, "", err
		}
		store, err := platformstore.NewFilesystemStore(platformstore.FilesystemStoreConfig{Root: root, StorageSecurityDomain: domain})
		if err != nil {
			return nil, "", fmt.Errorf("construct filesystem object store: %w", err)
		}
		return store, domain, nil
	case "s3":
		return newS3(ctx, cfg, instanceID, environment, options)
	default:
		return nil, "", fmt.Errorf("unsupported object-store backend %q", cfg.ObjectStoreBackend)
	}
}

// DeriveStorageSecurityDomain returns the canonical, stable identity for an
// application object-store namespace. Every input is length-bounded and
// encoded with an unambiguous delimiter before hashing.
func DeriveStorageSecurityDomain(instanceID, environment, providerNamespace, encryptionIdentity string) (string, error) {
	for name, value := range map[string]string{
		"instance ID": instanceID, "environment": environment,
		"provider namespace": providerNamespace, "encryption identity": encryptionIdentity,
	} {
		if strings.TrimSpace(value) != value || value == "" || strings.ContainsAny(value, "\x00\r\n") || len(value) > 4096 {
			return "", fmt.Errorf("invalid %s", name)
		}
	}
	h := sha256.New()
	for _, value := range []string{"leapview-object-store-v1", instanceID, environment, providerNamespace, encryptionIdentity} {
		// Length-prefixing avoids delimiter ambiguity while retaining a stable,
		// human-independent identity representation.
		fmt.Fprintf(h, "%d:", len(value))
		h.Write([]byte(value))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func filesystemRoot(cfg appconfig.Config) (string, error) {
	root := cfg.ObjectStoreFilesystemRoot
	if strings.TrimSpace(root) != root {
		return "", fmt.Errorf("object-store filesystem root must not contain surrounding whitespace")
	}
	if root == "" {
		root = filepath.Join(cfg.ArtifactDir(), "object-store")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve object-store filesystem root: %w", err)
	}
	abs = filepath.Clean(abs)
	if abs == string(filepath.Separator) {
		return "", fmt.Errorf("object-store filesystem root must be dedicated")
	}
	for name, candidate := range map[string]string{"LEAPVIEW_HOME": cfg.HomeDir, "LEAPVIEW_ARTIFACT_DIR": cfg.ArtifactDir()} {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		candidateAbs, err := filepath.Abs(candidate)
		if err == nil && filepath.Clean(candidateAbs) == abs {
			return "", fmt.Errorf("object-store filesystem root must be dedicated; it must not be %s", name)
		}
	}
	return abs, nil
}

func newS3(ctx context.Context, cfg appconfig.Config, instanceID, environment string, options Options) (platformstore.ImmutableStore, string, error) {
	if strings.TrimSpace(cfg.ObjectStoreS3Bucket) != cfg.ObjectStoreS3Bucket || strings.TrimSpace(cfg.ObjectStoreS3Region) != cfg.ObjectStoreS3Region || cfg.ObjectStoreS3Bucket == "" || cfg.ObjectStoreS3Region == "" {
		return nil, "", fmt.Errorf("S3 object storage requires bucket and region")
	}
	accessKey, secretKey, sessionToken := cfg.ObjectStoreS3AccessKeyID, cfg.ObjectStoreS3SecretAccessKey, cfg.ObjectStoreS3SessionToken
	if strings.TrimSpace(accessKey) != accessKey || strings.TrimSpace(secretKey) != secretKey || strings.TrimSpace(sessionToken) != sessionToken {
		return nil, "", fmt.Errorf("object-store S3 credentials must not contain surrounding whitespace")
	}
	if (strings.TrimSpace(accessKey) == "") != (strings.TrimSpace(secretKey) == "") {
		return nil, "", fmt.Errorf("S3 object-store static credentials require both access key ID and secret access key")
	}
	if strings.TrimSpace(sessionToken) != "" && (strings.TrimSpace(accessKey) == "" || strings.TrimSpace(secretKey) == "") {
		return nil, "", fmt.Errorf("S3 object-store session token requires static credentials")
	}
	endpoint, err := canonicalEndpoint(cfg.ObjectStoreS3Endpoint)
	if err != nil {
		return nil, "", err
	}
	prefix := cfg.ObjectStoreS3Prefix
	if strings.TrimSpace(prefix) != prefix {
		return nil, "", fmt.Errorf("object-store S3 prefix must not contain surrounding whitespace")
	}
	if prefix == "" {
		prefix = "objects"
	}
	mode, err := normalizeEncryptionMode(cfg.ObjectStoreS3EncryptionMode)
	if err != nil {
		return nil, "", err
	}
	enc := platformstore.S3EncryptionConfig{Mode: mode, OpaqueKeyRef: cfg.ObjectStoreS3EncryptionKeyRef, ProviderKey: cfg.ObjectStoreS3EncryptionProviderKey}
	if strings.TrimSpace(enc.OpaqueKeyRef) != enc.OpaqueKeyRef || strings.TrimSpace(enc.ProviderKey) != enc.ProviderKey {
		return nil, "", fmt.Errorf("object-store S3 encryption identities must not contain surrounding whitespace")
	}
	if mode == platformstore.S3EncryptionSSES3 {
		if enc.OpaqueKeyRef != "" || enc.ProviderKey != "" {
			return nil, "", fmt.Errorf("SSE-S3 object storage cannot carry KMS key identities")
		}
	} else if enc.OpaqueKeyRef == "" || enc.ProviderKey == "" || enc.OpaqueKeyRef == enc.ProviderKey {
		return nil, "", fmt.Errorf("SSE-KMS object storage requires distinct opaque and resolved provider key identities")
	}
	if strings.TrimSpace(cfg.ObjectStoreS3ExpectedBucketOwner) != cfg.ObjectStoreS3ExpectedBucketOwner {
		return nil, "", fmt.Errorf("object-store S3 expected bucket owner must not contain surrounding whitespace")
	}
	encryptionIdentity := string(mode) + "|opaque=" + enc.OpaqueKeyRef + "|provider=" + enc.ProviderKey
	namespace := "s3:" + strings.TrimSpace(cfg.ObjectStoreS3Bucket) + "|prefix=" + prefix + "|region=" + strings.TrimSpace(cfg.ObjectStoreS3Region) + "|endpoint=" + endpoint
	domain, err := DeriveStorageSecurityDomain(instanceID, environment, namespace, encryptionIdentity)
	if err != nil {
		return nil, "", err
	}
	load := options.LoadAWSConfig
	if load == nil {
		load = awsconfig.LoadDefaultConfig
	}
	loadOptions := make([]func(*awsconfig.LoadOptions) error, 0, 2)
	loadOptions = append(loadOptions, awsconfig.WithRegion(strings.TrimSpace(cfg.ObjectStoreS3Region)))
	if strings.TrimSpace(accessKey) != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, sessionToken)))
	}
	awsCfg, err := load(ctx, loadOptions...)
	if err != nil {
		return nil, "", fmt.Errorf("initialize object-store S3 client: %w", err)
	}
	build := options.NewS3Client
	if build == nil {
		build = func(config aws.Config, opts ...func(*awss3.Options)) platformstore.S3Client {
			return awss3.NewFromConfig(config, opts...)
		}
	}
	client := build(awsCfg, func(o *awss3.Options) {
		o.UsePathStyle = cfg.ObjectStoreS3PathStyle
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})
	store, err := platformstore.NewS3Store(client, platformstore.S3StoreConfig{Bucket: strings.TrimSpace(cfg.ObjectStoreS3Bucket), Prefix: prefix, StorageSecurityDomain: domain, Encryption: enc, ExpectedBucketOwner: strings.TrimSpace(cfg.ObjectStoreS3ExpectedBucketOwner)})
	if err != nil {
		return nil, "", fmt.Errorf("construct S3 object store: %w", err)
	}
	return store, domain, nil
}

func canonicalEndpoint(raw string) (string, error) {
	if strings.TrimSpace(raw) != raw {
		return "", fmt.Errorf("object-store S3 endpoint must not contain surrounding whitespace")
	}
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("object-store S3 endpoint must be an absolute URL without credentials, query, or fragment")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = path.Clean(parsed.Path)
	if parsed.Path == "." || parsed.Path == "/" {
		parsed.Path = ""
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func normalizeEncryptionMode(raw string) (platformstore.S3EncryptionMode, error) {
	switch raw {
	case "", "AES256":
		return platformstore.S3EncryptionSSES3, nil
	case "aws:kms":
		return platformstore.S3EncryptionSSEKMS, nil
	default:
		return "", fmt.Errorf("unsupported object-store S3 encryption mode %q", raw)
	}
}
