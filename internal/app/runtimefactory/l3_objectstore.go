package runtimefactory

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	analyticsl3 "github.com/flidai/leapview/internal/analytics/cache/l3"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/app/gcadapter"
)

// NewL3ObjectStore constructs the object-backed result-cache adapter from the
// admitted physical pool. Bucket and pool prefix are sourced exclusively from
// the contract; target-owned credentials and endpoint settings come from the
// supplied S3Config. Local pools intentionally return an error so callers can
// disable L3 explicitly instead of silently mixing storage domains.
func NewL3ObjectStore(ctx context.Context, contract *ducklake.PoolContract, config gcadapter.S3Config) (analyticsl3.ObjectStore, error) {
	if contract == nil || contract.Validate() != nil {
		return nil, fmt.Errorf("physical-pool admission is required")
	}
	if !strings.EqualFold(strings.TrimSpace(contract.Tuple.StorageImplementation), "s3") {
		return nil, fmt.Errorf("L3 object store requires an admitted S3 physical pool")
	}
	parsed, err := url.Parse(contract.Pool.Identity.StorageLocation)
	if err != nil || parsed.Scheme != "s3" || parsed.Host == "" {
		return nil, fmt.Errorf("physical-pool S3 location is invalid")
	}
	if strings.TrimSpace(config.AccessKeyID) == "" || strings.TrimSpace(config.SecretAccessKey) == "" {
		return nil, fmt.Errorf("target-owned S3 access and secret keys are required for L3 object store")
	}
	options := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(config.AccessKeyID, config.SecretAccessKey, config.SessionToken)),
	}
	if config.Region != "" {
		options = append(options, awsconfig.WithRegion(config.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, err
	}
	client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		o.UsePathStyle = config.PathStyle
		if config.Endpoint != "" {
			o.BaseEndpoint = &config.Endpoint
		}
	})
	prefix := strings.Trim(strings.Trim(parsed.Path, "/")+"/"+strings.Trim(contract.Pool.Identity.StorageNamespace, "/"), "/")
	encryptionRef := contract.Pool.Identity.EncryptionKeyRef
	providerKey := ""
	if encryptionRef != "" {
		if config.ResolveEncryptionKey == nil {
			return nil, fmt.Errorf("target-owned S3 encryption-key resolver is required for admitted encryption reference")
		}
		providerKey, err = config.ResolveEncryptionKey(ctx, encryptionRef)
		if err != nil {
			return nil, fmt.Errorf("resolve target-owned S3 encryption key: %w", err)
		}
	}
	return analyticsl3.NewS3ObjectStoreWithResolvedEncryption(client, parsed.Host, prefix, encryptionRef, providerKey, string(contract.Pool.ID))
}
