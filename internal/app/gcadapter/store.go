package gcadapter

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/deployment/gc"
	"github.com/flidai/leapview/internal/deployment/gcstore"
)

// S3Config contains target-owned connection settings for a physical pool.
// It carries no pool identity; bucket and prefix always come from the
// admitted pool contract.
type S3Config struct {
	Region, AccessKeyID, SecretAccessKey, SessionToken string
	Endpoint                                           string
	PathStyle                                          bool
}

// NewPoolStore selects a pool-scoped read/stat/list/delete adapter from the
// admitted physical-pool contract. Unsupported implementations fail closed;
// callers must never fall back to a process-wide mutable store.
func NewPoolStore(ctx context.Context, contract *ducklake.PoolContract, config S3Config) (gc.PoolStore, error) {
	if contract == nil {
		return nil, fmt.Errorf("physical-pool contract is required")
	}
	implementation := strings.ToLower(strings.TrimSpace(contract.Tuple.StorageImplementation))
	switch implementation {
	case "local", "filesystem":
		path, err := contract.Pool.DataPath()
		if err != nil {
			return nil, err
		}
		return gcstore.NewLocal(path)
	case "s3":
		location := contract.Pool.Identity.StorageLocation
		parsed, err := url.Parse(location)
		if err != nil || parsed.Host == "" || parsed.Scheme != "s3" {
			return nil, fmt.Errorf("physical-pool S3 location is invalid")
		}
		loadOptions := []func(*awsconfig.LoadOptions) error{}
		if strings.TrimSpace(config.Region) != "" {
			loadOptions = append(loadOptions, awsconfig.WithRegion(strings.TrimSpace(config.Region)))
		}
		if config.AccessKeyID != "" {
			loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(config.AccessKeyID, config.SecretAccessKey, config.SessionToken)))
		}
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
		if err != nil {
			return nil, fmt.Errorf("initialize physical-pool S3 client: %w", err)
		}
		client := awss3.NewFromConfig(awsCfg, func(options *awss3.Options) {
			options.UsePathStyle = config.PathStyle
			if endpoint := strings.TrimSpace(config.Endpoint); endpoint != "" {
				options.BaseEndpoint = &endpoint
			}
		})
		prefix := strings.Trim(strings.Trim(parsed.Path, "/")+"/"+strings.Trim(contract.Pool.Identity.StorageNamespace, "/"), "/")
		return gcstore.NewS3(client, parsed.Host, prefix)
	default:
		return nil, fmt.Errorf("unsupported physical-pool storage implementation %q", implementation)
	}
}
