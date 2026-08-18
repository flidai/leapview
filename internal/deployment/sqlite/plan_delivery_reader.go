package sqlite

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
)

// DeliveryOperatorSnapshot reads the durable control tables used by operator
// status. It intentionally projects no storage location, object key,
// credential reference, or raw evidence JSON. Scope filtering and all SQL are
// owned by the generated deployment queries.
func (r *Repository) DeliveryOperatorSnapshot(ctx context.Context, project, environment string) (deployment.DeliveryOperatorSnapshot, error) {
	if r == nil || r.db == nil {
		return deployment.DeliveryOperatorSnapshot{}, fmt.Errorf("delivery repository is unavailable")
	}
	queries := deploydb.New(r.db)
	result := deployment.DeliveryOperatorSnapshot{ProjectID: project, Environment: environment}
	target, err := queries.GetDeliveryOperatorTarget(ctx, deploydb.GetDeliveryOperatorTargetParams{ProjectID: project, Environment: environment})
	if err != nil {
		return deployment.DeliveryOperatorSnapshot{}, err
	}
	result.TargetID, result.TargetRevision, result.ActiveGeneration = target.TargetID, target.TargetRevision, target.ActiveGenerationID

	pools, err := queries.ListDeliveryOperatorPhysicalPoolAdmissions(ctx, deploydb.ListDeliveryOperatorPhysicalPoolAdmissionsParams{
		ProjectID: project, Environment: environment, ProjectID_2: project, Environment_2: environment, ProjectID_3: project, Environment_3: environment,
	})
	if err != nil {
		return deployment.DeliveryOperatorSnapshot{}, err
	}
	for _, row := range pools {
		admitted, err := parseDeliveryTime(row.AdmittedAt)
		if err != nil {
			return deployment.DeliveryOperatorSnapshot{}, err
		}
		result.PhysicalPools = append(result.PhysicalPools, deployment.DeliveryPhysicalPoolAdmissionView{
			PoolID: row.ID, IdentityDigest: row.IdentityDigest, CompatibilityDigest: row.CompatibilityDigest,
			EvidenceDigest: row.EvidenceDigest, ConformanceVersion: row.ConformanceVersion,
			DuckDBRuntime: operatorValueString(row.DuckdbRuntime), DuckLakeExtension: operatorValueString(row.DucklakeExtension),
			CatalogFormat: operatorValueString(row.CatalogFormat), StorageImplementation: operatorValueString(row.StorageImplementation),
			ObjectNamingContract: operatorValueString(row.ObjectNamingContract), AdmittedAt: admitted,
		})
	}

	roots, err := queries.ListDeliveryOperatorRoots(ctx, deploydb.ListDeliveryOperatorRootsParams{
		ProjectID: project, Environment: environment, ProjectID_2: project, Environment_2: environment,
		ProjectID_3: project, Environment_3: environment, ProjectID_4: project, Environment_4: environment,
		ProjectID_5: project, Environment_5: environment,
	})
	if err != nil {
		return deployment.DeliveryOperatorSnapshot{}, err
	}
	for _, row := range roots {
		created, err := parseDeliveryTime(row.CreatedAt)
		if err != nil {
			return deployment.DeliveryOperatorSnapshot{}, err
		}
		item := deployment.DeliveryRootView{PoolID: row.PhysicalPoolID, Kind: row.RootKind, SourceID: row.SourceID, CandidateID: row.CandidateID, GenerationID: row.GenerationID, LeaseID: row.LeaseID, CatalogDigest: row.CatalogDigest, Status: row.Status, CreatedAt: created}
		if row.ExpiresAt != "" {
			item.ExpiresAt, err = parseDeliveryTime(row.ExpiresAt)
			if err != nil {
				return deployment.DeliveryOperatorSnapshot{}, err
			}
		}
		result.Roots = append(result.Roots, item)
	}

	leases, err := queries.ListDeliveryOperatorQueryLeases(ctx, deploydb.ListDeliveryOperatorQueryLeasesParams{ProjectID: project, Environment: environment, ProjectID_2: project, Environment_2: environment})
	if err != nil {
		return deployment.DeliveryOperatorSnapshot{}, err
	}
	for _, row := range leases {
		created, err := parseDeliveryTime(row.CreatedAt)
		if err != nil {
			return deployment.DeliveryOperatorSnapshot{}, err
		}
		expires, err := parseDeliveryTime(row.ExpiresAt)
		if err != nil {
			return deployment.DeliveryOperatorSnapshot{}, err
		}
		result.QueryLeases = append(result.QueryLeases, deployment.DeliveryQueryLeaseView{ID: row.ID, HolderID: row.HolderID, CandidateID: row.CandidateID, GenerationID: row.GenerationID, PoolID: row.PhysicalPoolID, CatalogDigest: row.CatalogDigest, Status: row.Status, CreatedAt: created, ExpiresAt: expires})
	}

	writerLeases, err := queries.ListDeliveryOperatorWriterLeases(ctx, deploydb.ListDeliveryOperatorWriterLeasesParams{ProjectID: project, Environment: environment})
	if err != nil {
		return deployment.DeliveryOperatorSnapshot{}, err
	}
	for _, row := range writerLeases {
		created, err := parseDeliveryTime(row.CreatedAt)
		if err != nil {
			return deployment.DeliveryOperatorSnapshot{}, err
		}
		expires, err := parseDeliveryTime(row.ExpiresAt)
		if err != nil {
			return deployment.DeliveryOperatorSnapshot{}, err
		}
		item := deployment.DeliveryWriterLeaseView{ID: row.ID, AttemptID: row.AttemptID, PoolID: row.PhysicalPoolID, OwnerID: row.OwnerID, Epoch: row.Epoch, Status: row.Status, CreatedAt: created, ExpiresAt: expires}
		if row.ReleasedAt != "" {
			item.ReleasedAt, err = parseDeliveryTime(row.ReleasedAt)
			if err != nil {
				return deployment.DeliveryOperatorSnapshot{}, err
			}
		}
		result.WriterLeases = append(result.WriterLeases, item)
	}

	cycles, err := queries.ListDeliveryOperatorGCCycles(ctx, deploydb.ListDeliveryOperatorGCCyclesParams{
		ProjectID: project, Environment: environment, ProjectID_2: project, Environment_2: environment, ProjectID_3: project, Environment_3: environment,
	})
	if err != nil {
		return deployment.DeliveryOperatorSnapshot{}, err
	}
	for _, row := range cycles {
		created, err := parseDeliveryTime(row.CreatedAt)
		if err != nil {
			return deployment.DeliveryOperatorSnapshot{}, err
		}
		item := deployment.DeliveryGCCycleView{ID: row.ID, PoolID: row.PhysicalPoolID, Epoch: row.Epoch, RootRevision: row.RootRevision, MarkDigest: row.MarkDigest, Status: row.Status, CreatedAt: created, AbortReason: row.AbortReason}
		if row.CompletedAt != "" {
			item.CompletedAt, err = parseDeliveryTime(row.CompletedAt)
			if err != nil {
				return deployment.DeliveryOperatorSnapshot{}, err
			}
		}
		result.GCCycles = append(result.GCCycles, item)
	}

	intents, err := queries.ListDeliveryOperatorGCDeleteIntents(ctx, deploydb.ListDeliveryOperatorGCDeleteIntentsParams{
		ProjectID: project, Environment: environment, ProjectID_2: project, Environment_2: environment, ProjectID_3: project, Environment_3: environment,
	})
	if err != nil {
		return deployment.DeliveryOperatorSnapshot{}, err
	}
	for _, row := range intents {
		created, err := parseDeliveryTime(row.CreatedAt)
		if err != nil {
			return deployment.DeliveryOperatorSnapshot{}, err
		}
		item := deployment.DeliveryGCDeleteIntentView{ID: row.ID, CycleID: row.CycleID, PoolID: row.PhysicalPoolID, ObjectDigest: row.ObjectDigest, ObjectVersion: row.ObjectVersion, Status: row.Status, CreatedAt: created}
		if row.CompletedAt != "" {
			item.CompletedAt, err = parseDeliveryTime(row.CompletedAt)
			if err != nil {
				return deployment.DeliveryOperatorSnapshot{}, err
			}
		}
		result.GCDeleteIntents = append(result.GCDeleteIntents, item)
	}

	gcActive, err := queries.CountDeliveryOperatorActiveGCLeases(ctx, deploydb.CountDeliveryOperatorActiveGCLeasesParams{
		ProjectID: project, Environment: environment, ProjectID_2: project, Environment_2: environment, ProjectID_3: project, Environment_3: environment,
	})
	if err != nil {
		return deployment.DeliveryOperatorSnapshot{}, err
	}
	if gcActive > 0 {
		result.Degraded = true
		result.DegradedReasons = append(result.DegradedReasons, "gc_lease_active")
	}
	for _, cycle := range result.GCCycles {
		if cycle.Status == "aborted" {
			result.Degraded = true
			result.DegradedReasons = append(result.DegradedReasons, "gc_cycle_aborted")
			break
		}
	}
	return result, nil
}

func operatorValueString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return fmt.Sprint(value)
	}
}

var _ deployment.DeliveryReader = (*Repository)(nil)
