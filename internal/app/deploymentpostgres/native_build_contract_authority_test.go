package deploymentpostgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
)

func nativeContractDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

type nativeContractPhysicalFake struct {
	contract physicalpool.AdmissionContract
	err      error
	poolID   physicalpool.PoolID
	digest   string
	calls    int
}

func (f *nativeContractPhysicalFake) LoadAdmissionContractByCompatibilityDigest(_ context.Context, poolID physicalpool.PoolID, digest string) (physicalpool.AdmissionContract, error) {
	f.calls++
	f.poolID, f.digest = poolID, digest
	return f.contract, f.err
}

type nativeContractCatalogFake struct {
	identity ducklakepostgres.CatalogIdentity
	err      error
	poolID   string
	calls    int
}

func (f *nativeContractCatalogFake) LoadCatalog(_ context.Context, poolID string) (ducklakepostgres.CatalogIdentity, error) {
	f.calls++
	f.poolID = poolID
	return f.identity, f.err
}

type nativeContractRuntimeFake struct {
	compat           ducklakepostgres.CatalogRuntimeCompatibility
	err              error
	poolID           string
	calls            int
	eligibility      ducklakepostgres.RuntimeAttachEligibility
	eligibilityErr   error
	eligibilityIn    ducklakepostgres.RuntimeAttachInput
	eligibilityCalls int
}

func (f *nativeContractRuntimeFake) LoadCatalogRuntimeCompatibility(_ context.Context, poolID string) (ducklakepostgres.CatalogRuntimeCompatibility, error) {
	f.calls++
	f.poolID = poolID
	return f.compat, f.err
}

func (f *nativeContractRuntimeFake) CheckRuntimeAttachEligibility(_ context.Context, input ducklakepostgres.RuntimeAttachInput) (ducklakepostgres.RuntimeAttachEligibility, error) {
	f.eligibilityCalls++
	f.eligibilityIn = input
	if f.eligibilityErr != nil {
		return ducklakepostgres.RuntimeAttachEligibility{}, f.eligibilityErr
	}
	if f.eligibility.Current.PhysicalPoolID == "" {
		return ducklakepostgres.RuntimeAttachEligibility{Eligible: true, Reason: "qualified", Current: f.compat}, nil
	}
	return f.eligibility, nil
}

func TestNativeBuildContractAuthorityResolvesExactContract(t *testing.T) {
	fixture := newNativeBuildContractFixture(t)
	physical := &nativeContractPhysicalFake{contract: fixture.admission}
	catalog := &nativeContractCatalogFake{identity: fixture.catalog}
	runtime := &nativeContractRuntimeFake{compat: fixture.runtime}
	authority, err := NewNativeBuildContractAuthority(NativeBuildContractAuthorityConfig{
		PhysicalPool: physical, Catalog: catalog, Runtime: runtime,
		Domains: NativeBuildContractDomains{TenantDomain: "tenant-domain", EncryptionDomain: "encryption-domain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := authority.Resolve(t.Context(), NativeBuildContractRequest{PhysicalPoolID: fixture.pool.ID.String(), CompatibilityDigest: fixture.digest})
	if err != nil {
		t.Fatal(err)
	}
	if result.PoolContract == nil {
		t.Fatal("result omitted admitted pool contract")
	}
	if result.PoolContract.Pool.ID != fixture.pool.ID || result.PoolContract.Tuple != fixture.tuple {
		t.Fatalf("pool contract = %#v, want exact admitted contract", result.PoolContract)
	}
	if result.Catalog != fixture.catalog || result.CatalogRuntime != fixture.runtime || result.Compatibility != fixture.runtime.RuntimeCompatibility {
		t.Fatalf("result authority evidence differs: %#v", result)
	}
	if result.PhysicalPoolID != fixture.pool.ID.String() || result.CompatibilityDigest != fixture.digest || result.TenantDomain != "tenant-domain" || result.EncryptionDomain != "encryption-domain" || result.ObjectNamespace != fixture.pool.Identity.StorageNamespace {
		t.Fatalf("result identity = %#v", result)
	}
	if physical.calls != 1 || physical.poolID != fixture.pool.ID || physical.digest != fixture.digest || catalog.calls != 1 || catalog.poolID != fixture.pool.ID.String() || runtime.calls != 1 || runtime.poolID != fixture.pool.ID.String() || runtime.eligibilityCalls != 1 {
		t.Fatalf("exact lookups were not forwarded: physical=%#v catalog=%#v runtime=%#v", physical, catalog, runtime)
	}
	if runtime.eligibilityIn.PhysicalPoolID != fixture.pool.ID.String() || runtime.eligibilityIn.CatalogID != fixture.catalog.CatalogID || runtime.eligibilityIn.Compatibility != fixture.runtime.RuntimeCompatibility || runtime.eligibilityIn.AutomaticMigration {
		t.Fatalf("runtime eligibility input = %#v, want exact non-migrating attach", runtime.eligibilityIn)
	}
}

func TestNativeBuildContractAuthorityRejectsInvalidRequestBeforeLookup(t *testing.T) {
	fixture := newNativeBuildContractFixture(t)
	physical := &nativeContractPhysicalFake{contract: fixture.admission}
	authority := mustNativeBuildContractAuthority(t, physical, fixture)
	for name, request := range map[string]NativeBuildContractRequest{
		"missing pool":        {CompatibilityDigest: fixture.digest},
		"missing digest":      {PhysicalPoolID: fixture.pool.ID.String()},
		"pool not digest":     {PhysicalPoolID: "pool", CompatibilityDigest: fixture.digest},
		"uppercase digest":    {PhysicalPoolID: fixture.pool.ID.String(), CompatibilityDigest: strings.ToUpper(fixture.digest)},
		"trimmed pool digest": {PhysicalPoolID: " " + fixture.pool.ID.String(), CompatibilityDigest: fixture.digest},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := authority.Resolve(t.Context(), request); !errors.Is(err, deploymentnative.ErrInvalid) {
				t.Fatalf("error = %v, want deployment ErrInvalid", err)
			}
		})
	}
	if physical.calls != 0 {
		t.Fatalf("invalid requests performed %d authority lookups", physical.calls)
	}
}

func TestNativeBuildContractAuthorityRejectsAdmissionDrift(t *testing.T) {
	cases := map[string]func(*nativeBuildContractFixture){
		"pool id": func(f *nativeBuildContractFixture) {
			f.admission.Pool.ID = physicalpool.PoolID(nativeContractDigest('b'))
		},
		"admission pool id": func(f *nativeBuildContractFixture) {
			f.admission.Admission.PoolID = physicalpool.PoolID(nativeContractDigest('b'))
		},
		"compatibility digest": func(f *nativeBuildContractFixture) {
			f.admission.Admission.CompatibilityDigest = nativeContractDigest('b')
		},
		"evidence tuple": func(f *nativeBuildContractFixture) { f.admission.Evidence.Compatibility.CatalogFormat = "ducklake:v2" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newNativeBuildContractFixture(t)
			mutate(&fixture)
			physical := &nativeContractPhysicalFake{contract: fixture.admission}
			authority := mustNativeBuildContractAuthority(t, physical, fixture)
			if _, err := authority.Resolve(t.Context(), NativeBuildContractRequest{PhysicalPoolID: fixture.pool.ID.String(), CompatibilityDigest: fixture.digest}); !errors.Is(err, deploymentnative.ErrConflict) {
				t.Fatalf("error = %v, want conflict", err)
			}
		})
	}
}

func TestNativeBuildContractAuthorityRejectsCatalogAndRuntimeDrift(t *testing.T) {
	cases := map[string]func(*nativeBuildContractFixture){
		"catalog pool":       func(f *nativeBuildContractFixture) { f.catalog.PhysicalPoolID = "other-pool" },
		"catalog id runtime": func(f *nativeBuildContractFixture) { f.runtime.CatalogID = "other-catalog" },
		"catalog digest":     func(f *nativeBuildContractFixture) { f.catalog.CompatibilityDigest = nativeContractDigest('b') },
		"catalog schema":     func(f *nativeBuildContractFixture) { f.runtime.CatalogSchemaVersion = "schema-v2" },
		"runtime pool":       func(f *nativeBuildContractFixture) { f.runtime.PhysicalPoolID = "other-pool" },
		"runtime digest":     func(f *nativeBuildContractFixture) { f.runtime.CompatibilityDigest = nativeContractDigest('b') },
		"runtime tuple":      func(f *nativeBuildContractFixture) { f.runtime.DuckLakeExtension = "ducklake:v2" },
		"metadata schema":    func(f *nativeBuildContractFixture) { f.catalog.MetadataSchema = "other_schema" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newNativeBuildContractFixture(t)
			mutate(&fixture)
			physical := &nativeContractPhysicalFake{contract: fixture.admission}
			catalog := &nativeContractCatalogFake{identity: fixture.catalog}
			runtime := &nativeContractRuntimeFake{compat: fixture.runtime}
			authority := mustNativeBuildContractAuthorityWithReaders(t, physical, catalog, runtime, fixture)
			if _, err := authority.Resolve(t.Context(), NativeBuildContractRequest{PhysicalPoolID: fixture.pool.ID.String(), CompatibilityDigest: fixture.digest}); !errors.Is(err, deploymentnative.ErrConflict) {
				t.Fatalf("error = %v, want deployment ErrConflict", err)
			}
		})
	}
}

func TestNativeBuildContractAuthorityRejectsMissingCatalogRuntimeFields(t *testing.T) {
	cases := map[string]func(*nativeBuildContractFixture){
		"catalog pool":   func(f *nativeBuildContractFixture) { f.catalog.PhysicalPoolID = "" },
		"catalog id":     func(f *nativeBuildContractFixture) { f.catalog.CatalogID = "" },
		"catalog uuid":   func(f *nativeBuildContractFixture) { f.catalog.CatalogUUID = "00000000-0000-0000-0000-000000000000" },
		"catalog digest": func(f *nativeBuildContractFixture) { f.catalog.CompatibilityDigest = "" },
		"runtime pool":   func(f *nativeBuildContractFixture) { f.runtime.PhysicalPoolID = "" },
		"runtime id":     func(f *nativeBuildContractFixture) { f.runtime.CatalogID = "" },
		"runtime digest": func(f *nativeBuildContractFixture) { f.runtime.CompatibilityDigest = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newNativeBuildContractFixture(t)
			mutate(&fixture)
			physical := &nativeContractPhysicalFake{contract: fixture.admission}
			catalog := &nativeContractCatalogFake{identity: fixture.catalog}
			runtime := &nativeContractRuntimeFake{compat: fixture.runtime}
			authority := mustNativeBuildContractAuthorityWithReaders(t, physical, catalog, runtime, fixture)
			if _, err := authority.Resolve(t.Context(), NativeBuildContractRequest{PhysicalPoolID: fixture.pool.ID.String(), CompatibilityDigest: fixture.digest}); !errors.Is(err, deploymentnative.ErrInvalid) {
				t.Fatalf("error = %v, want deployment ErrInvalid", err)
			}
		})
	}
}

func TestNativeBuildContractAuthorityMapsMissingAndDependencyErrors(t *testing.T) {
	fixture := newNativeBuildContractFixture(t)
	tests := []struct {
		name string
		make func(*nativeBuildContractFixture) (NativeBuildContractPhysicalPoolAuthority, NativeBuildContractCatalogAuthority, NativeBuildContractRuntimeAuthority, error)
		want error
	}{
		{name: "missing admission", make: func(f *nativeBuildContractFixture) (NativeBuildContractPhysicalPoolAuthority, NativeBuildContractCatalogAuthority, NativeBuildContractRuntimeAuthority, error) {
			return &nativeContractPhysicalFake{err: physicalpool.ErrPoolNotAdmitted}, &nativeContractCatalogFake{identity: f.catalog}, &nativeContractRuntimeFake{compat: f.runtime}, nil
		}, want: ErrNativeBuildContractUnavailable},
		{name: "catalog not found", make: func(f *nativeBuildContractFixture) (NativeBuildContractPhysicalPoolAuthority, NativeBuildContractCatalogAuthority, NativeBuildContractRuntimeAuthority, error) {
			return &nativeContractPhysicalFake{contract: f.admission}, &nativeContractCatalogFake{err: ducklakepostgres.ErrNotFound}, &nativeContractRuntimeFake{compat: f.runtime}, nil
		}, want: ErrNativeBuildContractUnavailable},
		{name: "runtime invalid", make: func(f *nativeBuildContractFixture) (NativeBuildContractPhysicalPoolAuthority, NativeBuildContractCatalogAuthority, NativeBuildContractRuntimeAuthority, error) {
			return &nativeContractPhysicalFake{contract: f.admission}, &nativeContractCatalogFake{identity: f.catalog}, &nativeContractRuntimeFake{err: ducklakepostgres.ErrInvalid}, nil
		}, want: deploymentnative.ErrInvalid},
		{name: "dependency sentinel preserved", make: func(f *nativeBuildContractFixture) (NativeBuildContractPhysicalPoolAuthority, NativeBuildContractCatalogAuthority, NativeBuildContractRuntimeAuthority, error) {
			cause := errors.New("network down")
			return &nativeContractPhysicalFake{contract: f.admission}, &nativeContractCatalogFake{identity: f.catalog}, &nativeContractRuntimeFake{err: cause}, cause
		}, want: ErrNativeBuildContractUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			physical, catalog, runtime, cause := test.make(&fixture)
			authority := mustNativeBuildContractAuthorityWithReaders(t, physical, catalog, runtime, fixture)
			_, err := authority.Resolve(t.Context(), NativeBuildContractRequest{PhysicalPoolID: fixture.pool.ID.String(), CompatibilityDigest: fixture.digest})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, test.want)
			}
			if cause != nil && !errors.Is(err, cause) {
				t.Fatalf("error = %v, lost dependency sentinel", err)
			}
		})
	}
}

func TestNativeBuildContractAuthorityRequiresExactAttachEligibility(t *testing.T) {
	t.Run("checker error", func(t *testing.T) {
		fixture := newNativeBuildContractFixture(t)
		runtime := &nativeContractRuntimeFake{compat: fixture.runtime, eligibilityErr: ducklakepostgres.ErrRuntimeAttachIneligible}
		authority := mustNativeBuildContractAuthorityWithReaders(t, &nativeContractPhysicalFake{contract: fixture.admission}, &nativeContractCatalogFake{identity: fixture.catalog}, runtime, fixture)
		_, err := authority.Resolve(t.Context(), NativeBuildContractRequest{PhysicalPoolID: fixture.pool.ID.String(), CompatibilityDigest: fixture.digest})
		if !errors.Is(err, ErrNativeBuildContractUnavailable) || !errors.Is(err, ducklakepostgres.ErrRuntimeAttachIneligible) {
			t.Fatalf("error = %v, want unavailable runtime attach", err)
		}
	})

	t.Run("reported ineligible", func(t *testing.T) {
		fixture := newNativeBuildContractFixture(t)
		runtime := &nativeContractRuntimeFake{compat: fixture.runtime, eligibility: ducklakepostgres.RuntimeAttachEligibility{Eligible: false, Reason: "migration fence held", Current: fixture.runtime}}
		authority := mustNativeBuildContractAuthorityWithReaders(t, &nativeContractPhysicalFake{contract: fixture.admission}, &nativeContractCatalogFake{identity: fixture.catalog}, runtime, fixture)
		_, err := authority.Resolve(t.Context(), NativeBuildContractRequest{PhysicalPoolID: fixture.pool.ID.String(), CompatibilityDigest: fixture.digest})
		if !errors.Is(err, ErrNativeBuildContractUnavailable) || !errors.Is(err, ducklakepostgres.ErrRuntimeAttachIneligible) {
			t.Fatalf("error = %v, want unavailable runtime attach", err)
		}
	})

	for name, mutate := range map[string]func(*ducklakepostgres.RuntimeAttachEligibility){
		"current tuple drift": func(e *ducklakepostgres.RuntimeAttachEligibility) { e.Current.DuckDBRuntime = "duckdb:other" },
		"qualification epoch drift": func(e *ducklakepostgres.RuntimeAttachEligibility) {
			e.Current.CurrentMigrationID = "0198f2c0-7c7a-7f00-8a11-000000000099"
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newNativeBuildContractFixture(t)
			eligibility := ducklakepostgres.RuntimeAttachEligibility{Eligible: true, Reason: "qualified", Current: fixture.runtime}
			mutate(&eligibility)
			runtime := &nativeContractRuntimeFake{compat: fixture.runtime, eligibility: eligibility}
			authority := mustNativeBuildContractAuthorityWithReaders(t, &nativeContractPhysicalFake{contract: fixture.admission}, &nativeContractCatalogFake{identity: fixture.catalog}, runtime, fixture)
			if _, err := authority.Resolve(t.Context(), NativeBuildContractRequest{PhysicalPoolID: fixture.pool.ID.String(), CompatibilityDigest: fixture.digest}); !errors.Is(err, deploymentnative.ErrConflict) {
				t.Fatalf("error = %v, want conflict", err)
			}
		})
	}

	t.Run("missing qualification epoch", func(t *testing.T) {
		fixture := newNativeBuildContractFixture(t)
		fixture.runtime.CurrentMigrationID = ""
		runtime := &nativeContractRuntimeFake{compat: fixture.runtime}
		authority := mustNativeBuildContractAuthorityWithReaders(t, &nativeContractPhysicalFake{contract: fixture.admission}, &nativeContractCatalogFake{identity: fixture.catalog}, runtime, fixture)
		if _, err := authority.Resolve(t.Context(), NativeBuildContractRequest{PhysicalPoolID: fixture.pool.ID.String(), CompatibilityDigest: fixture.digest}); !errors.Is(err, deploymentnative.ErrInvalid) {
			t.Fatalf("error = %v, want invalid qualification epoch", err)
		}
	})
}

func TestNativeBuildContractAuthorityRequiresConfiguredDomains(t *testing.T) {
	fixture := newNativeBuildContractFixture(t)
	base := NativeBuildContractAuthorityConfig{PhysicalPool: &nativeContractPhysicalFake{contract: fixture.admission}, Catalog: &nativeContractCatalogFake{identity: fixture.catalog}, Runtime: &nativeContractRuntimeFake{compat: fixture.runtime}}
	for name, mutate := range map[string]func(*NativeBuildContractAuthorityConfig){
		"missing tenant":     func(c *NativeBuildContractAuthorityConfig) { c.Domains.EncryptionDomain = "enc" },
		"missing encryption": func(c *NativeBuildContractAuthorityConfig) { c.Domains.TenantDomain = "tenant-domain" },
		"noncanonical tenant": func(c *NativeBuildContractAuthorityConfig) {
			c.Domains = NativeBuildContractDomains{TenantDomain: " tenant-domain", EncryptionDomain: "enc"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			if _, err := NewNativeBuildContractAuthority(config); !errors.Is(err, deploymentnative.ErrInvalid) {
				t.Fatalf("error = %v, want deployment ErrInvalid", err)
			}
		})
	}
	config := base
	config.Domains = NativeBuildContractDomains{TenantDomain: "other-tenant", EncryptionDomain: "enc"}
	config.Runtime = base.Runtime
	authority, err := NewNativeBuildContractAuthority(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Resolve(t.Context(), NativeBuildContractRequest{PhysicalPoolID: fixture.pool.ID.String(), CompatibilityDigest: fixture.digest}); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("tenant drift error = %v, want deployment ErrConflict", err)
	}
}

func TestNativeBuildContractAuthorityRejectsIncompletePoolDomainsBeforeBuild(t *testing.T) {
	for name, mutate := range map[string]func(*physicalpool.PoolIdentity){
		"missing tenant":       func(identity *physicalpool.PoolIdentity) { identity.Tenant = "" },
		"missing region":       func(identity *physicalpool.PoolIdentity) { identity.Region = "" },
		"noncanonical prefix":  func(identity *physicalpool.PoolIdentity) { identity.StorageNamespace = "objects/" },
		"traversing namespace": func(identity *physicalpool.PoolIdentity) { identity.StorageNamespace = "../objects" },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newNativeBuildContractFixture(t)
			identity := fixture.pool.Identity
			mutate(&identity)
			rebindNativeBuildContractPool(t, &fixture, identity)
			physical := &nativeContractPhysicalFake{contract: fixture.admission}
			catalog := &nativeContractCatalogFake{identity: fixture.catalog}
			runtime := &nativeContractRuntimeFake{compat: fixture.runtime}
			authority := mustNativeBuildContractAuthorityWithReaders(t, physical, catalog, runtime, fixture)
			if _, err := authority.Resolve(t.Context(), NativeBuildContractRequest{PhysicalPoolID: fixture.pool.ID.String(), CompatibilityDigest: fixture.digest}); !errors.Is(err, deploymentnative.ErrInvalid) {
				t.Fatalf("error = %v, want invalid pool domain", err)
			}
		})
	}
}

type nativeBuildContractFixture struct {
	pool      physicalpool.PhysicalPool
	tuple     physicalpool.Compatibility
	digest    string
	admission physicalpool.AdmissionContract
	catalog   ducklakepostgres.CatalogIdentity
	runtime   ducklakepostgres.CatalogRuntimeCompatibility
}

func newNativeBuildContractFixture(t *testing.T) nativeBuildContractFixture {
	t.Helper()
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:v1", DuckLakeExtension: "ducklake:v1", CatalogFormat: "ducklake:v1", StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1"}
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{StorageLocation: t.TempDir(), StorageNamespace: "objects", Region: "us-east", Tenant: "tenant-domain", IsolationBoundary: "boundary", RetentionAuthority: "retention", Compatibility: tuple})
	if err != nil {
		t.Fatal(err)
	}
	checks := make([]physicalpool.EvidenceCheck, 0, len(ducklake.SharedPoolConformanceChecks))
	for _, name := range ducklake.SharedPoolConformanceChecks {
		checks = append(checks, physicalpool.EvidenceCheck{ID: name, Passed: true, ObservationDigest: nativeContractDigest('a')})
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: tuple, ConformanceVersion: ducklake.SharedPoolConformanceVersion, Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := pool.Admit(evidence)
	if err != nil {
		t.Fatal(err)
	}
	pool, err = pool.ApplyAdmission(admitted)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := tuple.Digest()
	if err != nil {
		t.Fatal(err)
	}
	catalog := ducklakepostgres.CatalogIdentity{PhysicalPoolID: pool.ID.String(), CatalogDatabase: "ducklake", CatalogID: "catalog-native", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000001", MetadataSchema: ducklake.MetadataSchemaForPool(pool.ID.String()), CompatibilityDigest: digest, CatalogSchemaVersion: "schema-v1"}
	runtime := ducklakepostgres.CatalogRuntimeCompatibility{PhysicalPoolID: pool.ID.String(), CatalogID: catalog.CatalogID, RuntimeCompatibility: ducklakepostgres.RuntimeCompatibility{RuntimeTuple: ducklakepostgres.RuntimeTuple{DuckDBRuntime: tuple.DuckDBRuntime, DuckLakeExtension: tuple.DuckLakeExtension, CatalogFormat: tuple.CatalogFormat}, CompatibilityDigest: digest, CatalogSchemaVersion: catalog.CatalogSchemaVersion}, CurrentMigrationID: "0198f2c0-7c7a-7f00-8a11-000000000010"}
	return nativeBuildContractFixture{pool: pool, tuple: tuple, digest: digest, admission: physicalpool.AdmissionContract{Pool: pool, Admission: admitted, Evidence: evidence}, catalog: catalog, runtime: runtime}
}

func rebindNativeBuildContractPool(t *testing.T, fixture *nativeBuildContractFixture, identity physicalpool.PoolIdentity) {
	t.Helper()
	pool, err := physicalpool.NewPhysicalPool(identity)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := pool.Admit(fixture.admission.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	pool, err = pool.ApplyAdmission(admitted)
	if err != nil {
		t.Fatal(err)
	}
	fixture.pool = pool
	fixture.admission = physicalpool.AdmissionContract{Pool: pool, Admission: admitted, Evidence: fixture.admission.Evidence}
	fixture.catalog.PhysicalPoolID = pool.ID.String()
	fixture.catalog.MetadataSchema = ducklake.MetadataSchemaForPool(pool.ID.String())
	fixture.runtime.PhysicalPoolID = pool.ID.String()
}

func mustNativeBuildContractAuthority(t *testing.T, physical NativeBuildContractPhysicalPoolAuthority, fixture nativeBuildContractFixture) *NativeBuildContractAuthority {
	t.Helper()
	return mustNativeBuildContractAuthorityWithReaders(t, physical, &nativeContractCatalogFake{identity: fixture.catalog}, &nativeContractRuntimeFake{compat: fixture.runtime}, fixture)
}

func mustNativeBuildContractAuthorityWithReaders(t *testing.T, physical NativeBuildContractPhysicalPoolAuthority, catalog NativeBuildContractCatalogAuthority, runtime NativeBuildContractRuntimeAuthority, fixture nativeBuildContractFixture) *NativeBuildContractAuthority {
	t.Helper()
	authority, err := NewNativeBuildContractAuthority(NativeBuildContractAuthorityConfig{PhysicalPool: physical, Catalog: catalog, Runtime: runtime, Domains: NativeBuildContractDomains{TenantDomain: "tenant-domain", EncryptionDomain: "encryption-domain"}})
	if err != nil {
		t.Fatal(err)
	}
	return authority
}
