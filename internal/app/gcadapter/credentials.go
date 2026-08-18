package gcadapter

import (
	"context"
	"database/sql/driver"
	"fmt"
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/analytics/ducklake"
)

// NewPoolCredentialBootstrap returns the target-owned, per-connection
// bootstrap required before DuckLake attaches an object-backed pool. It never
// uses ambient DuckDB credentials: an S3 pool without explicit target-owned
// keys fails closed. Local pools do not need a connector secret.
func NewPoolCredentialBootstrap(contract *ducklake.PoolContract, config S3Config) (ducklake.CredentialBootstrap, error) {
	if contract == nil {
		return nil, fmt.Errorf("physical-pool contract is required")
	}
	if strings.ToLower(strings.TrimSpace(contract.Tuple.StorageImplementation)) != "s3" {
		return nil, nil
	}
	if strings.TrimSpace(config.AccessKeyID) == "" || strings.TrimSpace(config.SecretAccessKey) == "" {
		return nil, fmt.Errorf("target-owned S3 access and secret keys are required for DuckLake credential bootstrap")
	}
	if location := strings.TrimSpace(contract.Pool.Identity.StorageLocation); location == "" {
		return nil, fmt.Errorf("S3 physical-pool storage location is required for DuckLake credential bootstrap")
	} else if parsed, err := url.Parse(location); err != nil || parsed.Scheme != "s3" || parsed.Host == "" {
		return nil, fmt.Errorf("S3 physical-pool storage location is invalid")
	}
	endpoint, useSSL, err := bootstrapEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	region := strings.TrimSpace(config.Region)
	accessKey, secretKey, sessionToken := config.AccessKeyID, config.SecretAccessKey, config.SessionToken
	pathStyle := config.PathStyle
	return func(ctx context.Context, execer driver.ExecerContext) error {
		if _, err := execer.ExecContext(ctx, "LOAD httpfs", nil); err != nil {
			return err
		}
		parts := []string{
			"TYPE S3",
			"KEY_ID '" + sqlLiteral(accessKey) + "'",
			"SECRET '" + sqlLiteral(secretKey) + "'",
		}
		if sessionToken != "" {
			parts = append(parts, "SESSION_TOKEN '"+sqlLiteral(sessionToken)+"'")
		}
		if region != "" {
			parts = append(parts, "REGION '"+sqlLiteral(region)+"'")
		}
		if endpoint != "" {
			parts = append(parts, "ENDPOINT '"+sqlLiteral(endpoint)+"'", fmt.Sprintf("USE_SSL %t", useSSL))
		}
		if pathStyle {
			parts = append(parts, "URL_STYLE 'path'")
		}
		_, err := execer.ExecContext(ctx, "CREATE OR REPLACE SECRET leapview_pool ("+strings.Join(parts, ", ")+")", nil)
		return err
	}, nil
}

func bootstrapEndpoint(value string) (string, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false, fmt.Errorf("S3 endpoint is invalid")
	}
	return parsed.Host + strings.TrimSuffix(parsed.Path, "/"), parsed.Scheme == "https", nil
}

func sqlLiteral(value string) string { return strings.ReplaceAll(value, "'", "''") }
