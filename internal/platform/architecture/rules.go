package architecture

import (
	"fmt"
	"strings"
)

// Layer describes the architectural role of a Go package. The classification
// is intentionally data, rather than test code, so every architecture check
// evaluates the same package model.
type Layer string

const (
	LayerContract    Layer = "contract"
	LayerUseCase     Layer = "use_case"
	LayerAdapter     Layer = "adapter"
	LayerModule      Layer = "module"
	LayerComposition Layer = "composition"
	LayerPlatform    Layer = "platform"
)

// PackageRule assigns an internal package prefix to one accountable owner and
// layer. The longest matching prefix wins, which permits narrow transitional
// ownership declarations without weakening the general capability rule.
type PackageRule struct {
	Prefix     string
	Capability string
	Layer      Layer
}

// PublicContractPrefixes declares the only packages another capability may
// import as a synchronous contract. Adapter and module packages are never
// made public merely because their capability has an allowed edge.
var PublicContractPrefixes = map[string][]string{
	"access":       {"internal/access", "internal/access/api", "internal/access/policy", "internal/access/snapshot", "internal/access/ui/signals"},
	"agent":        {"internal/agent/api", "internal/agent/ui/signals"},
	"analytics":    {"internal/analytics/model", "internal/analytics/query", "internal/analytics/materialize", "internal/analytics/materialization", "internal/analytics/connectors", "internal/analytics/arrowquery", "internal/analytics/resource", "internal/analytics/runtime", "internal/analytics/queryaudit", "internal/analytics/dataquery"},
	"dashboard":    {"internal/dashboard", "internal/dashboard/api", "internal/dashboard/appearance", "internal/dashboard/catalog", "internal/dashboard/definition", "internal/dashboard/filter", "internal/dashboard/publication", "internal/dashboard/report", "internal/dashboard/reportmodel", "internal/dashboard/queryruntime", "internal/dashboard/ui/signals", "internal/dashboard/visualization/definition", "internal/dashboard/visualization/format", "internal/dashboard/visualization/geometry", "internal/dashboard/visualization/ir", "internal/dashboard/visualization/mapasset", "internal/dashboard/visualization/runtime"},
	"manageddata":  {"internal/manageddata", "internal/manageddata/binding", "internal/manageddata/runtimebinding"},
	"workspace":    {"internal/workspace", "internal/workspace/api", "internal/workspace/navigation", "internal/workspace/search"},
	"project":      {"internal/project", "internal/project/api", "internal/project/schema", "internal/project/artifact", "internal/project/bundle", "internal/project/compiler"},
	"release":      {"internal/release"},
	"deployment":   {"internal/deployment"},
	"servingstate": {"internal/servingstate", "internal/servingstate/validate", "internal/servingstate/validation", "internal/servingstate/retention"},
	"refresh":      {"internal/refresh/artifact", "internal/refresh/plan", "internal/refresh/run", "internal/refresh/schedule"},
	"runtimehost":  {"internal/runtimehost"},
	"workload":     {"internal/workload"},
}

// DeferredPackageEdges are the explicit ownership exceptions retained for
// later roadmap slices. They are package-scoped so a deferred compiler,
// refresh-state, or persistence concern cannot silently authorize the same
// edge from unrelated production code.
type DeferredPackageEdge struct {
	SourcePrefix string
	Target       string
	RoadmapSlice int
}

var DeferredPackageEdges = []DeferredPackageEdge{}

var WorkloadImportPrefixes = []string{
	"internal/app", "internal/app/cli", "internal/app/config", "internal/integration", "internal/app/tools",
	"internal/platform/jobs", "internal/admin/storage", "internal/refresh/run",
	"internal/admin/module",
	"internal/dashboard/module",
	"internal/refresh/module",
	"internal/servingstate/module",
	"internal/workload/observability",
	"internal/workload/module",
	"internal/analytics/materialize", "internal/analytics/ducklake", "internal/dashboard/semanticapi",
}

func AllowsWorkloadImport(packagePath string) bool {
	for _, prefix := range WorkloadImportPrefixes {
		if packagePath == prefix || strings.HasPrefix(packagePath, prefix+"/") {
			return true
		}
	}
	return false
}

func IsDeferredPackageEdge(sourcePath, targetCapability string) bool {
	for _, edge := range DeferredPackageEdges {
		if edge.Target == targetCapability && (sourcePath == edge.SourcePrefix || strings.HasPrefix(sourcePath, edge.SourcePrefix+"/")) {
			return true
		}
	}
	return false
}

var CapabilityDependencies = map[string]map[string]bool{
	"project":      {"workspace": true, "analytics": true, "dashboard": true, "access": true, "refresh": true, "servingstate": true},
	"workspace":    {"access": true, "analytics": true, "dashboard": true},
	"access":       {},
	"manageddata":  {"servingstate": true},
	"analytics":    {"access": true, "manageddata": true, "servingstate": true},
	"dashboard":    {"access": true, "analytics": true, "runtimehost": true, "workload": true},
	"agent":        {"access": true, "analytics": true, "dashboard": true, "workspace": true, "project": true},
	"admin":        {"access": true, "agent": true, "analytics": true, "dashboard": true, "workspace": true},
	"release":      {"access": true, "project": true, "workspace": true, "servingstate": true},
	"deployment":   {"access": true, "project": true, "release": true, "servingstate": true, "manageddata": true, "runtimehost": true},
	"servingstate": {"access": true, "workload": true},
	"refresh":      {"access": true, "servingstate": true, "manageddata": true, "analytics": true, "runtimehost": true, "workspace": true, "workload": true},
	"runtimehost":  {"manageddata": true, "servingstate": true},
	"workload":     {},
	"platform":     {},
}

func IsPublicContractImport(capability, packagePath string) bool {
	for _, prefix := range PublicContractPrefixes[capability] {
		if packagePath == prefix {
			return true
		}
		// A capability root names only its root contract package. Explicit
		// nested contract prefixes may include their own child packages.
		if strings.Count(prefix, "/") > 1 && strings.HasPrefix(packagePath, prefix+"/") {
			return true
		}
	}
	return false
}

// CapabilityImportViolation validates one cross-capability production import.
// Module packages deliberately use the same rules as every other capability
// package; only process composition is exempt.
func CapabilityImportViolation(sourcePath string, source PackageRule, packagePath string, target PackageRule) string {
	if source.Capability == target.Capability || source.Layer == LayerComposition {
		return ""
	}
	if target.Capability == "platform" {
		return ""
	}
	if target.Capability == "workload" && AllowsWorkloadImport(sourcePath) {
		return ""
	}
	if IsDeferredPackageEdge(sourcePath, target.Capability) {
		return ""
	}
	if !CapabilityDependencies[source.Capability][target.Capability] {
		return fmt.Sprintf("undeclared capability edge %s -> %s", source.Capability, target.Capability)
	}
	if target.Layer == LayerAdapter {
		return fmt.Sprintf("adapter package %s owned by capability %s", packagePath, target.Capability)
	}
	if !IsPublicContractImport(target.Capability, packagePath) {
		return fmt.Sprintf("non-contract package %s from capability %s", packagePath, target.Capability)
	}
	return ""
}

var PackageRules = []PackageRule{
	{Prefix: "cmd", Capability: "composition", Layer: LayerComposition},
	{Prefix: "docs", Capability: "composition", Layer: LayerAdapter},
	{Prefix: "site", Capability: "composition", Layer: LayerAdapter},
	{Prefix: "desktop/native/windowspolicy", Capability: "platform", Layer: LayerAdapter},
	{Prefix: "pkg/agent", Capability: "agent", Layer: LayerContract},
	{Prefix: "internal/project/compiler", Capability: "project", Layer: LayerUseCase},
	{Prefix: "internal/project/artifact", Capability: "project", Layer: LayerContract},
	{Prefix: "internal/analytics/runtime", Capability: "analytics", Layer: LayerContract},
	{Prefix: "internal/analytics/infisical", Capability: "analytics", Layer: LayerAdapter},
	{Prefix: "internal/analytics/environment", Capability: "analytics", Layer: LayerAdapter},
	{Prefix: "internal/dashboard/analyticsruntime", Capability: "dashboard", Layer: LayerAdapter},
	{Prefix: "internal/refresh/analyticsruntime", Capability: "refresh", Layer: LayerAdapter},
	{Prefix: "internal/refresh/plan", Capability: "refresh", Layer: LayerUseCase},
	{Prefix: "internal/refresh/run", Capability: "refresh", Layer: LayerUseCase},
	{Prefix: "internal/refresh/schedule", Capability: "refresh", Layer: LayerUseCase},
	{Prefix: "internal/platform/http/idempotency", Capability: "platform", Layer: LayerAdapter},
	{Prefix: "internal/platform/http/api/gen", Capability: "platform", Layer: LayerAdapter},
	{Prefix: "internal/access/api/gen", Capability: "access", Layer: LayerAdapter},
	{Prefix: "internal/agent/api/gen", Capability: "agent", Layer: LayerAdapter},
	{Prefix: "internal/analytics/api/gen", Capability: "analytics", Layer: LayerAdapter},
	{Prefix: "internal/dashboard/api/gen", Capability: "dashboard", Layer: LayerAdapter},
	{Prefix: "internal/deployment/api/gen", Capability: "deployment", Layer: LayerAdapter},
	{Prefix: "internal/manageddata/api/gen", Capability: "manageddata", Layer: LayerAdapter},
	{Prefix: "internal/project/api/gen", Capability: "project", Layer: LayerAdapter},
	{Prefix: "internal/refresh/api/gen", Capability: "refresh", Layer: LayerAdapter},
	{Prefix: "internal/release/api/gen", Capability: "release", Layer: LayerAdapter},
	{Prefix: "internal/workspace/api/gen", Capability: "workspace", Layer: LayerAdapter},
	{Prefix: "internal/app/api/aggregate", Capability: "composition", Layer: LayerAdapter},
	{Prefix: "internal/app/api/gen", Capability: "composition", Layer: LayerAdapter},
	{Prefix: "internal/platform/architecture", Capability: "platform", Layer: LayerPlatform},
	{Prefix: "internal/workspace/assetnav", Capability: "workspace", Layer: LayerUseCase},
	{Prefix: "internal/workspace/navigation", Capability: "workspace", Layer: LayerContract},
	{Prefix: "internal/app/brand", Capability: "composition", Layer: LayerAdapter},
	{Prefix: "internal/dashboard/catalog", Capability: "dashboard", Layer: LayerContract},
	{Prefix: "internal/dashboard/appearance", Capability: "dashboard", Layer: LayerContract},
	{Prefix: "internal/app/cli", Capability: "composition", Layer: LayerComposition},
	{Prefix: "internal/app/cli/composectl", Capability: "composition", Layer: LayerComposition},
	{Prefix: "internal/app/config", Capability: "composition", Layer: LayerComposition},
	{Prefix: "internal/project/schema", Capability: "project", Layer: LayerContract},
	{Prefix: "internal/platform/http/cursorsigning", Capability: "platform", Layer: LayerAdapter},
	{Prefix: "internal/analytics/dataquery", Capability: "analytics", Layer: LayerContract},
	{Prefix: "internal/project/docvalidation", Capability: "project", Layer: LayerUseCase},
	{Prefix: "internal/platform/locking", Capability: "platform", Layer: LayerAdapter},
	{Prefix: "internal/platform/observability", Capability: "platform", Layer: LayerAdapter},
	{Prefix: "internal/dashboard/observability", Capability: "dashboard", Layer: LayerAdapter},
	{Prefix: "internal/workload/observability", Capability: "workload", Layer: LayerAdapter},
	{Prefix: "internal/dashboard/queryruntime", Capability: "dashboard", Layer: LayerContract},
	{Prefix: "internal/workspace/search", Capability: "workspace", Layer: LayerUseCase},
	{Prefix: "internal/platform/securestore", Capability: "platform", Layer: LayerPlatform},
	{Prefix: "internal/platform/security/secret", Capability: "platform", Layer: LayerPlatform},
	{Prefix: "internal/platform/filesystem", Capability: "platform", Layer: LayerPlatform},
	{Prefix: "internal/app/site", Capability: "composition", Layer: LayerAdapter},
	{Prefix: "internal/platform/web/staticasset", Capability: "platform", Layer: LayerPlatform},
	{Prefix: "internal/app/tools", Capability: "composition", Layer: LayerComposition},
	{Prefix: "internal/dashboard/visualization/definition", Capability: "dashboard", Layer: LayerContract},
	{Prefix: "internal/dashboard/visualization/format", Capability: "dashboard", Layer: LayerContract},
	{Prefix: "internal/dashboard/visualization/geometry", Capability: "dashboard", Layer: LayerContract},
	{Prefix: "internal/dashboard/visualization/ir", Capability: "dashboard", Layer: LayerContract},
	{Prefix: "internal/dashboard/visualization/mapasset", Capability: "dashboard", Layer: LayerContract},
	{Prefix: "internal/dashboard/visualization/mapasset/http", Capability: "dashboard", Layer: LayerAdapter},
	{Prefix: "internal/dashboard/visualization/runtime", Capability: "dashboard", Layer: LayerUseCase},
	{Prefix: "internal/app/site/visualdocs", Capability: "composition", Layer: LayerAdapter},
	{Prefix: "internal/dashboard/semanticapi", Capability: "dashboard", Layer: LayerAdapter},
	{Prefix: "internal/access/ui/signals", Capability: "access", Layer: LayerContract},
	{Prefix: "internal/admin/offline", Capability: "admin", Layer: LayerUseCase},
	{Prefix: "internal/admin/ui/signals", Capability: "admin", Layer: LayerContract},
	{Prefix: "internal/agent/ui/signals", Capability: "agent", Layer: LayerContract},
	{Prefix: "internal/dashboard/ui/signals", Capability: "dashboard", Layer: LayerContract},
	{Prefix: "internal/app", Capability: "composition", Layer: LayerComposition},
	{Prefix: "internal/admin", Capability: "admin", Layer: LayerAdapter},
	{Prefix: "internal/workspace/ui/signals", Capability: "workspace", Layer: LayerContract},
	{Prefix: "internal/workspace/ui", Capability: "workspace", Layer: LayerAdapter},
}

var adapterSegments = []string{
	"/http", "/cli", "/sqlite", "/internal/db", "/filesystem", "/s3", "/tus", "/duckdb", "/ducklake", "/datastar", "/openai", "/ui",
}

// ClassifyPackage returns the owner and layer for an internal package path.
func ClassifyPackage(path string) (PackageRule, bool) {
	path = strings.TrimSuffix(path, "/")
	best := PackageRule{}
	found := false
	for _, rule := range PackageRules {
		if path != rule.Prefix && !strings.HasPrefix(path, rule.Prefix+"/") {
			continue
		}
		if !found || len(rule.Prefix) > len(best.Prefix) {
			best, found = rule, true
		}
	}
	if found {
		if best.Layer != LayerComposition && strings.HasSuffix(path, "/module") {
			best.Layer = LayerModule
		}
		return best, true
	}
	const internal = "internal/"
	if !strings.HasPrefix(path, internal) {
		return PackageRule{}, false
	}
	remainder := strings.TrimPrefix(path, internal)
	owner := strings.SplitN(remainder, "/", 2)[0]
	if _, ok := CapabilityDependencies[owner]; !ok {
		return PackageRule{}, false
	}
	layer := LayerUseCase
	if path == internal+owner {
		layer = LayerContract
	}
	if strings.HasSuffix(path, "/module") {
		layer = LayerModule
	} else {
		for _, segment := range adapterSegments {
			if strings.Contains(path, segment) {
				layer = LayerAdapter
				break
			}
		}
	}
	return PackageRule{Prefix: internal + owner, Capability: owner, Layer: layer}, true
}

func HasExplicitPackageRule(path string) bool {
	for _, rule := range PackageRules {
		if path == rule.Prefix || strings.HasPrefix(path, rule.Prefix+"/") {
			return true
		}
	}
	return false
}
