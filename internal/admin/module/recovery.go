package module

import (
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/platform"
)

// ExternalRecoveryPoint is the canonical operator-owned external recovery
// identity consumed by both offline restore and scheduled qualification.
type ExternalRecoveryPoint = adminoffline.ExternalRecoveryPoint

// RecoveryStorageConfig is the non-secret storage identity required to build
// the canonical FAI-515 backup topology.
type RecoveryStorageConfig struct {
	ManagedDataBackend    string
	ManagedDataS3Endpoint string
	ManagedDataS3Region   string
	ManagedDataS3Bucket   string
	ManagedDataS3Prefix   string
}

// BuildRecoveryStorageTopology exposes the offline owner's canonical topology
// projection through the admin module boundary used by application composition.
func BuildRecoveryStorageTopology(
	config RecoveryStorageConfig,
	points []ExternalRecoveryPoint,
	requireRecoveryPoint bool,
) (platform.InstanceBackupStorageTopology, error) {
	topology, err := adminoffline.BuildStorageTopology(adminoffline.Config{
		ManagedDataBackend: config.ManagedDataBackend, ManagedDataS3Endpoint: config.ManagedDataS3Endpoint,
		ManagedDataS3Region: config.ManagedDataS3Region, ManagedDataS3Bucket: config.ManagedDataS3Bucket,
		ManagedDataS3Prefix: config.ManagedDataS3Prefix,
	}, points, requireRecoveryPoint)
	if err != nil {
		return platform.InstanceBackupStorageTopology{}, err
	}
	external := make([]platform.InstanceBackupExternalStoreReference, len(topology.ExternalStores))
	for index, reference := range topology.ExternalStores {
		external[index] = platform.InstanceBackupExternalStoreReference{
			Role: reference.Role, Provider: reference.Provider, Endpoint: reference.Endpoint,
			Region: reference.Region, Bucket: reference.Bucket, Prefix: reference.Prefix,
			RecoveryPoint: reference.RecoveryPoint, EvidenceKey: reference.EvidenceKey,
		}
	}
	return platform.InstanceBackupStorageTopology{
		ControlPlane: topology.ControlPlane, ManagedData: topology.ManagedData,
		DuckLake: topology.DuckLake, ExternalStores: external,
	}, nil
}
