package offline

import (
	"fmt"
	"net/url"
	"strings"
)

// BuildStorageTopology is the canonical FAI-515 projection from deployment
// storage configuration plus native-store recovery evidence into manifest v2.
// Scheduled qualification and offline operator workflows must share this path.
func BuildStorageTopology(config Config, points []ExternalRecoveryPoint, requireRecoveryPoint bool) (BackupStorageTopology, error) {
	topology := BackupStorageTopology{ControlPlane: "local", ManagedData: "local", DuckLake: "local", ExternalStores: []BackupExternalStoreReference{}}
	backend := strings.TrimSpace(config.ManagedDataBackend)
	switch backend {
	case "", "local":
		if len(points) != 0 {
			return BackupStorageTopology{}, fmt.Errorf("external recovery points were supplied for local managed-data storage")
		}
		return topology, nil
	case "s3":
		region := strings.TrimSpace(config.ManagedDataS3Region)
		if region == "" {
			return BackupStorageTopology{}, fmt.Errorf("S3 managed-data storage requires a configured region")
		}
		bucket := strings.TrimSpace(config.ManagedDataS3Bucket)
		if bucket == "" {
			return BackupStorageTopology{}, fmt.Errorf("S3 managed-data storage requires a configured bucket")
		}
		provider := "aws"
		endpoint := strings.TrimSpace(config.ManagedDataS3Endpoint)
		if endpoint == "" {
			endpoint = "https://s3." + region + ".amazonaws.com"
		} else {
			provider = "s3-compatible"
		}
		parsedEndpoint, err := url.Parse(endpoint)
		if err != nil || (parsedEndpoint.Scheme != "http" && parsedEndpoint.Scheme != "https") || parsedEndpoint.Host == "" ||
			parsedEndpoint.User != nil || parsedEndpoint.RawQuery != "" || parsedEndpoint.Fragment != "" ||
			(parsedEndpoint.Path != "" && parsedEndpoint.Path != "/") {
			return BackupStorageTopology{}, fmt.Errorf("S3 managed-data endpoint must be a credential-free HTTP(S) origin")
		}
		parsedEndpoint.Scheme = strings.ToLower(parsedEndpoint.Scheme)
		parsedEndpoint.Host = strings.ToLower(parsedEndpoint.Host)
		parsedEndpoint.Path = ""
		endpoint = parsedEndpoint.String()
		var managed *ExternalRecoveryPoint
		for index := range points {
			if points[index].Role != "managed-data" {
				return BackupStorageTopology{}, fmt.Errorf("unsupported external recovery role %q", points[index].Role)
			}
			if managed != nil {
				return BackupStorageTopology{}, fmt.Errorf("managed-data external recovery point is duplicated")
			}
			managed = &points[index]
		}
		if requireRecoveryPoint && (managed == nil || strings.TrimSpace(managed.RecoveryPoint) == "" || strings.TrimSpace(managed.EvidenceKey) == "") {
			return BackupStorageTopology{}, fmt.Errorf("S3 managed-data backup requires an exact external recovery point and evidence key")
		}
		prefix := strings.Trim(strings.TrimSpace(config.ManagedDataS3Prefix), "/")
		topology.ManagedData = "external"
		reference := BackupExternalStoreReference{
			Role: "managed-data", Provider: provider, Endpoint: endpoint, Region: region, Bucket: bucket, Prefix: prefix,
		}
		if managed != nil {
			reference.RecoveryPoint = strings.TrimSpace(managed.RecoveryPoint)
			reference.EvidenceKey = strings.TrimSpace(managed.EvidenceKey)
		}
		topology.ExternalStores = []BackupExternalStoreReference{reference}
		return topology, nil
	default:
		return BackupStorageTopology{}, fmt.Errorf("unsupported managed-data backend %q", backend)
	}
}
