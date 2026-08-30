package deploymentpostgres

// This file owns the read-only contract resolution boundary used by a native
// BuildPlan coordinator.  Resolution is deliberately keyed by the caller's
// exact physical-pool ID and compatibility digest: there is no latest row,
// fallback tuple, or process-local default involved in this operation.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolpostgres "github.com/flidai/leapview/internal/analytics/physicalpool/postgres"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/google/uuid"
)

const (
	nativeBuildContractMaxID     = 255
	nativeBuildContractMaxDigest = 128
	nativeBuildContractMaxText   = 512
)

var (
	// ErrNativeBuildContractUnavailable means that an exact authority record or
	// configured dependency could not be read. Keep this application-boundary
	// sentinel because the native deployment package currently has no
	// unavailable classification.
	ErrNativeBuildContractUnavailable = errors.New("native build physical-pool contract authority is unavailable")
)

// NativeBuildContractPhysicalPoolAuthority is the exact admission lookup
// required by this boundary.  In production it is implemented by
// physicalpoolpostgres.Repository.
type NativeBuildContractPhysicalPoolAuthority interface {
	LoadAdmissionContractByCompatibilityDigest(context.Context, physicalpool.PoolID, string) (physicalpool.AdmissionContract, error)
}

// NativeBuildContractCatalogAuthority loads the catalog identity bound to a
// physical pool.  It intentionally exposes no list/latest operation.
type NativeBuildContractCatalogAuthority interface {
	LoadCatalog(context.Context, string) (ducklakepostgres.CatalogIdentity, error)
}

// NativeBuildContractRuntimeAuthority loads the exact catalog/runtime row for
// a physical pool.  It intentionally exposes no list/latest operation.
type NativeBuildContractRuntimeAuthority interface {
	LoadCatalogRuntimeCompatibility(context.Context, string) (ducklakepostgres.CatalogRuntimeCompatibility, error)
	CheckRuntimeAttachEligibility(context.Context, ducklakepostgres.RuntimeAttachInput) (ducklakepostgres.RuntimeAttachEligibility, error)
}

var (
	_ NativeBuildContractPhysicalPoolAuthority = (*physicalpoolpostgres.Repository)(nil)
	_ NativeBuildContractCatalogAuthority      = (*ducklakepostgres.Repository)(nil)
	_ NativeBuildContractRuntimeAuthority      = (*ducklakepostgres.Repository)(nil)
)

// NativeBuildContractDomains are target-owned values which are not represented
// exactly by physicalpool.PoolIdentity. TenantDomain is checked against the
// identity's optional Tenant field. EncryptionDomain must be configured here;
// EncryptionKeyRef is an opaque key handle, not a security-domain identity,
// and therefore is never silently reused. ObjectNamespace is derived exactly
// from the required StorageNamespace.
type NativeBuildContractDomains struct {
	TenantDomain     string
	EncryptionDomain string
}

// NativeBuildContractAuthorityConfig supplies the three narrow PostgreSQL
// readers and explicit target domains.
type NativeBuildContractAuthorityConfig struct {
	PhysicalPool NativeBuildContractPhysicalPoolAuthority
	Catalog      NativeBuildContractCatalogAuthority
	Runtime      NativeBuildContractRuntimeAuthority
	Domains      NativeBuildContractDomains
}

// NativeBuildContractAuthority is read-only and stateless. It never caches a
// latest admission and never opens a second authority behind these interfaces.
type NativeBuildContractAuthority struct {
	physicalPool NativeBuildContractPhysicalPoolAuthority
	catalog      NativeBuildContractCatalogAuthority
	runtime      NativeBuildContractRuntimeAuthority
	domains      NativeBuildContractDomains
}

// NewNativeBuildContractAuthority validates dependency presence and explicit
// domains without performing I/O.
func NewNativeBuildContractAuthority(config NativeBuildContractAuthorityConfig) (*NativeBuildContractAuthority, error) {
	if config.PhysicalPool == nil || config.Catalog == nil || config.Runtime == nil {
		return nil, fmt.Errorf("%w: native build contract authorities are required", deploymentnative.ErrInvalid)
	}
	if err := validateNativeBuildContractDomains(config.Domains); err != nil {
		return nil, err
	}
	return &NativeBuildContractAuthority{physicalPool: config.PhysicalPool, catalog: config.Catalog, runtime: config.Runtime, domains: config.Domains}, nil
}

// NativeBuildContractRequest names one exact immutable compatibility record.
// PhysicalPoolID and CompatibilityDigest are both mandatory; a resolver must
// not infer either from a catalog, runtime row, or process configuration.
type NativeBuildContractRequest struct {
	PhysicalPoolID      string
	CompatibilityDigest string
}

// NativeBuildContract is the cross-authority result consumed by a future
// native BuildPlan coordinator. PoolContract is reconstructed from the exact
// admitted contract, never from a latest physical-pool row.
type NativeBuildContract struct {
	PhysicalPoolID      string
	CompatibilityDigest string
	PoolContract        *ducklake.PoolContract
	Catalog             ducklakepostgres.CatalogIdentity
	CatalogRuntime      ducklakepostgres.CatalogRuntimeCompatibility
	Compatibility       ducklakepostgres.RuntimeCompatibility
	TenantDomain        string
	EncryptionDomain    string
	ObjectNamespace     string
}

// Resolve reads and cross-checks one exact admission, catalog identity, and
// catalog/runtime compatibility row.
func (a *NativeBuildContractAuthority) Resolve(ctx context.Context, request NativeBuildContractRequest) (NativeBuildContract, error) {
	if a == nil || a.physicalPool == nil || a.catalog == nil || a.runtime == nil {
		return NativeBuildContract{}, fmt.Errorf("%w: native build contract authorities are not configured", ErrNativeBuildContractUnavailable)
	}
	if err := validateNativeBuildContractRequest(request); err != nil {
		return NativeBuildContract{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	poolID := physicalpool.PoolID(request.PhysicalPoolID)
	admission, err := a.physicalPool.LoadAdmissionContractByCompatibilityDigest(ctx, poolID, request.CompatibilityDigest)
	if err != nil {
		return NativeBuildContract{}, mapNativeBuildContractDependencyError("load physical-pool admission", err)
	}
	if err := validateNativeBuildContractAdmission(admission, request); err != nil {
		return NativeBuildContract{}, err
	}

	catalog, err := a.catalog.LoadCatalog(ctx, request.PhysicalPoolID)
	if err != nil {
		return NativeBuildContract{}, mapNativeBuildContractDependencyError("load DuckLake catalog identity", err)
	}
	if err := validateNativeBuildContractCatalog(catalog, request, admission); err != nil {
		return NativeBuildContract{}, err
	}

	runtime, err := a.runtime.LoadCatalogRuntimeCompatibility(ctx, request.PhysicalPoolID)
	if err != nil {
		return NativeBuildContract{}, mapNativeBuildContractDependencyError("load DuckLake catalog runtime compatibility", err)
	}
	if err := validateNativeBuildContractRuntime(runtime, catalog, request, admission); err != nil {
		return NativeBuildContract{}, err
	}
	eligibility, err := a.runtime.CheckRuntimeAttachEligibility(ctx, ducklakepostgres.RuntimeAttachInput{
		PhysicalPoolID: request.PhysicalPoolID,
		CatalogID:      catalog.CatalogID,
		Compatibility:  runtime.RuntimeCompatibility,
		// Runtime authorities must never infer permission to migrate while a
		// product build is attaching a writer.
		AutomaticMigration: false,
	})
	if err != nil {
		return NativeBuildContract{}, fmt.Errorf("%w: check DuckLake runtime attach eligibility: %w", ErrNativeBuildContractUnavailable, err)
	}
	if err := validateNativeBuildContractEligibility(eligibility, runtime); err != nil {
		return NativeBuildContract{}, err
	}

	if err := validateNativeBuildContractDomainsForPool(a.domains, admission.Pool); err != nil {
		return NativeBuildContract{}, err
	}
	contract := &ducklake.PoolContract{
		Pool: admission.Pool, Tuple: admission.Admission.Compatibility,
		Admission: admission.Admission, Evidence: admission.Evidence,
	}
	if err := contract.Validate(); err != nil {
		return NativeBuildContract{}, fmt.Errorf("%w: admitted physical-pool contract: %v", deploymentnative.ErrInvalid, err)
	}
	return NativeBuildContract{
		PhysicalPoolID: request.PhysicalPoolID, CompatibilityDigest: request.CompatibilityDigest,
		PoolContract: contract, Catalog: catalog, CatalogRuntime: runtime,
		Compatibility: runtime.RuntimeCompatibility,
		TenantDomain:  a.domains.TenantDomain, EncryptionDomain: a.domains.EncryptionDomain,
		ObjectNamespace: admission.Pool.Identity.StorageNamespace,
	}, nil
}

func validateNativeBuildContractRequest(request NativeBuildContractRequest) error {
	if err := validateNativeBuildContractDigest(request.PhysicalPoolID, "physical pool id", nativeBuildContractMaxID); err != nil {
		return err
	}
	if err := validateNativeBuildContractDigest(request.CompatibilityDigest, "compatibility digest", nativeBuildContractMaxDigest); err != nil {
		return err
	}
	return nil
}

func validateNativeBuildContractAdmission(admission physicalpool.AdmissionContract, request NativeBuildContractRequest) error {
	if admission.Pool.ID == "" || admission.Admission.PoolID == "" {
		return fmt.Errorf("%w: admitted physical-pool identity is missing", deploymentnative.ErrInvalid)
	}
	// Compare the requested immutable identity before validating the loaded
	// value. A different, but otherwise well-formed, row is a conflict rather
	// than an invalid request.
	if admission.Pool.ID.String() != request.PhysicalPoolID || admission.Admission.PoolID.String() != request.PhysicalPoolID {
		return fmt.Errorf("%w: admission physical-pool identity differs", deploymentnative.ErrConflict)
	}
	if admission.Admission.CompatibilityDigest != request.CompatibilityDigest {
		return fmt.Errorf("%w: admission compatibility digest differs", deploymentnative.ErrConflict)
	}
	if admission.Admission.Compatibility != admission.Evidence.Compatibility {
		return fmt.Errorf("%w: admission evidence tuple differs", deploymentnative.ErrConflict)
	}
	if err := admission.Pool.Validate(); err != nil {
		return fmt.Errorf("%w: admitted physical-pool identity: %v", deploymentnative.ErrInvalid, err)
	}
	if err := admission.Admission.Validate(); err != nil {
		return fmt.Errorf("%w: physical-pool admission: %v", deploymentnative.ErrInvalid, err)
	}
	computed, err := admission.Admission.Compatibility.Digest()
	if err != nil {
		return fmt.Errorf("%w: admission compatibility tuple: %v", deploymentnative.ErrInvalid, err)
	}
	if computed != request.CompatibilityDigest {
		return fmt.Errorf("%w: admission compatibility tuple digest differs", deploymentnative.ErrConflict)
	}
	return nil
}

func validateNativeBuildContractCatalog(catalog ducklakepostgres.CatalogIdentity, request NativeBuildContractRequest, admission physicalpool.AdmissionContract) error {
	if catalog.PhysicalPoolID == "" {
		return fmt.Errorf("%w: catalog physical-pool identity is missing", deploymentnative.ErrInvalid)
	}
	if catalog.PhysicalPoolID != request.PhysicalPoolID {
		return fmt.Errorf("%w: catalog physical-pool identity differs", deploymentnative.ErrConflict)
	}
	if err := validateNativeBuildContractText(catalog.CatalogDatabase, "catalog database", nativeBuildContractMaxID); err != nil {
		return err
	}
	if err := validateNativeBuildContractText(catalog.CatalogID, "catalog id", nativeBuildContractMaxID); err != nil {
		return err
	}
	parsedUUID, err := uuid.Parse(catalog.CatalogUUID)
	if err != nil || parsedUUID == uuid.Nil || parsedUUID.String() != catalog.CatalogUUID {
		return fmt.Errorf("%w: catalog UUID is not canonical", deploymentnative.ErrInvalid)
	}
	if err := validateNativeBuildContractText(catalog.MetadataSchema, "catalog metadata schema", nativeBuildContractMaxID); err != nil {
		return err
	}
	if catalog.MetadataSchema != ducklake.MetadataSchemaForPool(request.PhysicalPoolID) {
		return fmt.Errorf("%w: catalog metadata schema differs from physical pool", deploymentnative.ErrConflict)
	}
	if err := validateNativeBuildContractText(catalog.CatalogSchemaVersion, "catalog schema version", nativeBuildContractMaxID); err != nil {
		return err
	}
	if err := validateNativeBuildContractDigest(catalog.CompatibilityDigest, "catalog compatibility digest", nativeBuildContractMaxDigest); err != nil {
		return err
	}
	if catalog.CompatibilityDigest != request.CompatibilityDigest || catalog.CompatibilityDigest != admission.Admission.CompatibilityDigest {
		return fmt.Errorf("%w: catalog compatibility digest differs", deploymentnative.ErrConflict)
	}
	return nil
}

func validateNativeBuildContractRuntime(runtime ducklakepostgres.CatalogRuntimeCompatibility, catalog ducklakepostgres.CatalogIdentity, request NativeBuildContractRequest, admission physicalpool.AdmissionContract) error {
	if runtime.PhysicalPoolID == "" {
		return fmt.Errorf("%w: catalog-runtime physical-pool identity is missing", deploymentnative.ErrInvalid)
	}
	if runtime.PhysicalPoolID != request.PhysicalPoolID || runtime.PhysicalPoolID != catalog.PhysicalPoolID {
		return fmt.Errorf("%w: catalog-runtime physical-pool identity differs", deploymentnative.ErrConflict)
	}
	if runtime.CatalogID == "" {
		return fmt.Errorf("%w: catalog-runtime catalog identity is missing", deploymentnative.ErrInvalid)
	}
	if runtime.CatalogID != catalog.CatalogID {
		return fmt.Errorf("%w: catalog-runtime catalog identity differs", deploymentnative.ErrConflict)
	}
	if err := validateNativeBuildContractText(runtime.CatalogID, "catalog-runtime catalog id", nativeBuildContractMaxID); err != nil {
		return err
	}
	if err := validateNativeBuildContractRuntimeCompatibility(runtime.RuntimeCompatibility); err != nil {
		return err
	}
	if runtime.CompatibilityDigest != request.CompatibilityDigest || runtime.CompatibilityDigest != admission.Admission.CompatibilityDigest {
		return fmt.Errorf("%w: catalog-runtime compatibility digest differs", deploymentnative.ErrConflict)
	}
	if runtime.CatalogSchemaVersion != catalog.CatalogSchemaVersion {
		return fmt.Errorf("%w: catalog-runtime catalog schema version differs", deploymentnative.ErrConflict)
	}
	admitted := admission.Admission.Compatibility
	if runtime.DuckDBRuntime != admitted.DuckDBRuntime || runtime.DuckLakeExtension != admitted.DuckLakeExtension || runtime.CatalogFormat != admitted.CatalogFormat {
		return fmt.Errorf("%w: catalog-runtime compatibility tuple differs", deploymentnative.ErrConflict)
	}
	return nil
}

func validateNativeBuildContractRuntimeCompatibility(value ducklakepostgres.RuntimeCompatibility) error {
	fields := []struct {
		label  string
		value  string
		digest bool
	}{
		{label: "DuckDB runtime", value: value.DuckDBRuntime},
		{label: "DuckLake extension", value: value.DuckLakeExtension},
		{label: "catalog format", value: value.CatalogFormat},
		{label: "compatibility digest", value: value.CompatibilityDigest, digest: true},
		{label: "catalog schema version", value: value.CatalogSchemaVersion},
	}
	for _, field := range fields {
		if field.digest {
			if err := validateNativeBuildContractDigest(field.value, field.label, nativeBuildContractMaxDigest); err != nil {
				return err
			}
			continue
		}
		if err := validateNativeBuildContractText(field.value, field.label, nativeBuildContractMaxID); err != nil {
			return err
		}
	}
	return nil
}

func validateNativeBuildContractEligibility(eligibility ducklakepostgres.RuntimeAttachEligibility, runtime ducklakepostgres.CatalogRuntimeCompatibility) error {
	if !eligibility.Eligible {
		reason := strings.TrimSpace(eligibility.Reason)
		if reason == "" || len(reason) > nativeBuildContractMaxText || !utf8.ValidString(reason) || strings.IndexFunc(reason, unicode.IsControl) >= 0 {
			reason = "checker reported ineligible"
		}
		return fmt.Errorf("%w: %w: %s", ErrNativeBuildContractUnavailable, ducklakepostgres.ErrRuntimeAttachIneligible, reason)
	}
	current := eligibility.Current
	if current.PhysicalPoolID != runtime.PhysicalPoolID || current.CatalogID != runtime.CatalogID || current.RuntimeCompatibility != runtime.RuntimeCompatibility || current.CurrentMigrationID != runtime.CurrentMigrationID {
		return fmt.Errorf("%w: runtime attach eligibility identity differs", deploymentnative.ErrConflict)
	}
	qualificationID, err := uuid.Parse(current.CurrentMigrationID)
	if err != nil || qualificationID == uuid.Nil || qualificationID.String() != current.CurrentMigrationID {
		return fmt.Errorf("%w: runtime attach qualification epoch is invalid", deploymentnative.ErrInvalid)
	}
	return nil
}

func validateNativeBuildContractDomains(domains NativeBuildContractDomains) error {
	if err := validateNativeBuildContractText(domains.TenantDomain, "tenant domain", nativeBuildContractMaxText); err != nil {
		return err
	}
	if err := validateNativeBuildContractText(domains.EncryptionDomain, "encryption domain", nativeBuildContractMaxText); err != nil {
		return err
	}
	return nil
}

func validateNativeBuildContractDomainsForPool(domains NativeBuildContractDomains, pool physicalpool.PhysicalPool) error {
	identity := pool.Identity
	if err := validateNativeBuildContractText(identity.Tenant, "physical-pool tenant", nativeBuildContractMaxText); err != nil {
		return err
	}
	if identity.Tenant != domains.TenantDomain {
		return fmt.Errorf("%w: configured tenant domain differs from physical-pool tenant", deploymentnative.ErrConflict)
	}
	if err := validateNativeBuildContractText(identity.Region, "physical-pool region", nativeBuildContractMaxText); err != nil {
		return err
	}
	if err := validateNativeBuildContractText(identity.StorageNamespace, "physical-pool storage namespace", nativeBuildContractMaxText); err != nil {
		return err
	}
	canonicalNamespace := path.Clean(strings.Trim(identity.StorageNamespace, "/"))
	if canonicalNamespace == "." || canonicalNamespace != identity.StorageNamespace {
		return fmt.Errorf("%w: physical-pool storage namespace is not canonical", deploymentnative.ErrInvalid)
	}
	if _, err := pool.DataPath(); err != nil {
		return fmt.Errorf("%w: physical-pool data path: %v", deploymentnative.ErrInvalid, err)
	}
	return nil
}

func validateNativeBuildContractDigest(value, label string, max int) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > max {
		return fmt.Errorf("%w: %s is not canonical", deploymentnative.ErrInvalid, label)
	}
	if err := platformdigest.ValidateSHA256Identity(value); err != nil {
		return fmt.Errorf("%w: %s is invalid: %v", deploymentnative.ErrInvalid, label, err)
	}
	return nil
}

func validateNativeBuildContractText(value, label string, max int) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > max || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: %s is invalid", deploymentnative.ErrInvalid, label)
	}
	return nil
}

func mapNativeBuildContractDependencyError(stage string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s", ErrNativeBuildContractUnavailable, stage)
	}
	kind := ErrNativeBuildContractUnavailable
	switch {
	case errors.Is(err, physicalpool.ErrInvalidPool), errors.Is(err, physicalpool.ErrInvalidCompatibility), errors.Is(err, physicalpool.ErrEvidenceInvalid), errors.Is(err, ducklakepostgres.ErrInvalid):
		kind = deploymentnative.ErrInvalid
	case errors.Is(err, physicalpool.ErrCompatibilityMismatch), errors.Is(err, physicalpool.ErrPoolMismatch), errors.Is(err, ducklakepostgres.ErrConflict), errors.Is(err, ducklakepostgres.ErrCompatibilityMismatch):
		kind = deploymentnative.ErrConflict
	case errors.Is(err, physicalpool.ErrPoolNotAdmitted), errors.Is(err, ducklakepostgres.ErrNotFound):
		kind = ErrNativeBuildContractUnavailable
	}
	return fmt.Errorf("%w: %s: %w", kind, stage, err)
}
