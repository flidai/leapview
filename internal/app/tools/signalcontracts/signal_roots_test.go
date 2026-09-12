package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
)

// signalRootEvidence is deliberately source-anchored. A root is not
// considered covered merely because it is present in a generated model: the
// manifest below names the producer and the browser reader (or a reviewed
// exception for roots which are intentionally one-sided).
type signalRootEvidence struct {
	producer sourceMarker
	reader   sourceMarker
	allow    string
}

type sourceMarker struct {
	path   string
	marker string
}

func rootMarker(path string, root string) sourceMarker {
	return sourceMarker{path: path, marker: root}
}

func signalRootEvidenceCatalog() map[string]signalRootEvidence {
	return map[string]signalRootEvidence{
		"adminAccess":                {producer: rootMarker("internal/admin/http/handler.go", "adminAccess"), reader: rootMarker("web/components/admin/settings-surfaces.ts", "adminAccess")},
		"adminAgentCommand":          {producer: rootMarker("internal/admin/ui/page.go", "adminAgentCommand"), reader: rootMarker("web/components/admin/admin-page.ts", "adminAgentCommand")},
		"adminAuditLog":              {producer: rootMarker("internal/admin/http/handler.go", "adminAuditLog"), reader: rootMarker("web/components/admin/settings-surfaces.ts", "adminAuditLog")},
		"adminProjects":              {reader: rootMarker("web/components/admin/settings-surfaces.ts", "adminProjects"), allow: "consumer-only"},
		"adminQueryDetail":           {producer: rootMarker("internal/admin/http/query_history.go", "adminQueryDetail"), reader: rootMarker("web/components/admin/admin-page.ts", "adminQueryDetail")},
		"adminQueryHistory":          {producer: rootMarker("internal/admin/http/query_history.go", "adminQueryHistory"), reader: rootMarker("web/components/admin/admin-page.ts", "adminQueryHistory")},
		"adminServiceAccounts":       {producer: rootMarker("internal/admin/http/handler.go", "adminServiceAccounts"), reader: rootMarker("web/components/admin/settings-surfaces.ts", "adminServiceAccounts")},
		"agent":                      {producer: rootMarker("internal/agent/ui/signals.go", "agent"), reader: rootMarker("web/components/chat/chat-drawer.ts", "agent")},
		"agentContext":               {producer: rootMarker("internal/agent/ui/signals.go", "agentContext"), reader: rootMarker("web/components/chat/chat-drawer.ts", "agentContext")},
		"agentReferenceSearch":       {producer: rootMarker("internal/agent/ui/signals.go", "agentReferenceSearch"), reader: rootMarker("web/components/chat/chat-drawer.ts", "agentReferenceSearch")},
		"agentTurnPending":           {reader: rootMarker("web/components/chat/chat-drawer.ts", "agentTurnPending"), allow: "browser"},
		"agentVisuals":               {producer: rootMarker("internal/agent/http/chat.go", "agentVisuals"), reader: rootMarker("web/components/chat/chat-drawer.ts", "agentVisuals")},
		"assetVersionDrawer":         {reader: rootMarker("web/components/project/project-page.ts", "assetVersionDrawer"), allow: "surface-specific"},
		"builder":                    {producer: rootMarker("internal/dashboard/ui/builder_page.go", "builder"), reader: rootMarker("web/components/dashboard/dashboard-builder.ts", "builder")},
		"builderFilterCommand":       {producer: rootMarker("internal/dashboard/ui/builder_page.go", "builderFilterCommand"), allow: "surface-specific"},
		"builderFilterContract":      {producer: rootMarker("internal/dashboard/ui/builder_page.go", "builderFilterContract"), reader: rootMarker("web/components/dashboard/dashboard-builder.ts", "builderFilterContract")},
		"builderFilterOptionPages":   {producer: rootMarker("internal/dashboard/ui/builder_page.go", "builderFilterOptionPages"), reader: rootMarker("web/components/dashboard/dashboard-builder.ts", "builderFilterOptionPages")},
		"builderFilterOptionRequest": {producer: rootMarker("internal/dashboard/ui/builder_page.go", "builderFilterOptionRequest"), allow: "surface-specific"},
		"builderFilterState":         {producer: rootMarker("internal/dashboard/ui/builder_page.go", "builderFilterState"), reader: rootMarker("web/components/dashboard/dashboard-builder.ts", "builderFilterState")},
		"builderFilterValidation":    {producer: rootMarker("internal/dashboard/ui/builder_page.go", "builderFilterValidation"), reader: rootMarker("web/components/dashboard/dashboard-builder.ts", "builderFilterValidation")},
		"builderVisuals":             {producer: rootMarker("internal/dashboard/ui/builder_page.go", "builderVisuals"), reader: rootMarker("web/components/dashboard/dashboard-builder.ts", "builderVisuals")},
		"chrome":                     {producer: rootMarker("internal/platform/web/page/page.go", "chrome"), reader: rootMarker("web/components/app/app-shell.ts", "chrome")},
		"connectionAdmin":            {producer: rootMarker("internal/project/http/creator_commands.go", "connectionAdmin"), reader: rootMarker("web/components/project/project-page.ts", "connectionAdmin")},
		"dataExplorer":               {producer: rootMarker("internal/project/http/browser.go", "dataExplorer"), reader: rootMarker("web/components/data/data-explorer.ts", "dataExplorer")},
		"filterCommand":              {producer: rootMarker("internal/dashboard/ui/page.go", "filterCommand"), allow: "surface-specific"},
		"filterContract":             {producer: rootMarker("internal/dashboard/ui/page.go", "filterContract"), reader: rootMarker("web/components/dashboard/dashboard-page.ts", "filterContract")},
		"filterOptionPages":          {producer: rootMarker("internal/dashboard/ui/page.go", "filterOptionPages"), reader: rootMarker("web/components/dashboard/dashboard-page.ts", "filterOptionPages")},
		"filterOptionRequest":        {producer: rootMarker("internal/dashboard/ui/page.go", "filterOptionRequest"), allow: "surface-specific"},
		"filterState":                {producer: rootMarker("internal/dashboard/http/filter_commands.go", "filterState"), reader: rootMarker("web/components/dashboard/dashboard-page.ts", "filterState")},
		"filterValidation":           {producer: rootMarker("internal/dashboard/ui/page.go", "filterValidation"), reader: rootMarker("web/components/dashboard/dashboard-page.ts", "filterValidation")},
		"interactionCommand":         {producer: rootMarker("internal/dashboard/ui/page.go", "interactionCommand"), allow: "surface-specific"},
		"interactionRevision":        {producer: rootMarker("internal/dashboard/ui/draft_preview_page.go", "interactionRevision"), reader: rootMarker("web/components/dashboard/dashboard-page.ts", "interactionRevision")},
		"interactionSelections":      {producer: rootMarker("internal/dashboard/ui/draft_preview_page.go", "interactionSelections"), reader: rootMarker("web/components/dashboard/dashboard-page.ts", "interactionSelections")},
		"modelFieldDrawer":           {reader: rootMarker("web/components/project/project-page.ts", "modelFieldDrawer"), allow: "surface-specific"},
		"navigationCommand":          {producer: rootMarker("internal/dashboard/ui/page.go", "navigationCommand"), allow: "surface-specific"},
		"page":                       {producer: rootMarker("internal/dashboard/ui/page.go", "page"), reader: rootMarker("web/components/dashboard/dashboard-page.ts", "page")},
		"personalSettings":           {producer: rootMarker("internal/admin/personalsettings/ui.go", "personalSettings"), reader: rootMarker("web/components/admin/personal-settings.ts", "personalSettings")},
		"pipelineCommand":            {producer: rootMarker("internal/project/http/creator_commands.go", "pipelineCommand"), reader: rootMarker("web/components/project/pipelines-page.ts", "pipelineCommand")},
		"pipelineCommandStatus":      {producer: rootMarker("internal/project/http/creator_commands.go", "pipelineCommandStatus"), reader: rootMarker("web/components/project/pipelines-page.ts", "pipelineCommandStatus")},
		"productSettings":            {producer: rootMarker("internal/admin/http/handler.go", "productSettings"), reader: rootMarker("web/components/admin/product-settings.ts", "productSettings")},
		"refreshRunDrawer":           {reader: rootMarker("web/components/project/project-page.ts", "refreshRunDrawer"), allow: "surface-specific"},
		"runtime":                    {producer: rootMarker("internal/dashboard/ui/page.go", "runtime"), reader: rootMarker("web/components/dashboard/dashboard-page.ts", "runtime")},
		"spatialInteractionCommand":  {producer: rootMarker("internal/dashboard/ui/page.go", "spatialInteractionCommand"), allow: "surface-specific"},
		"spatialSelections":          {producer: rootMarker("internal/dashboard/ui/draft_preview_page.go", "spatialSelections"), reader: rootMarker("web/components/dashboard/dashboard-page.ts", "spatialSelections")},
		"status":                     {producer: rootMarker("internal/dashboard/ui/page.go", "status"), reader: rootMarker("web/components/dashboard/dashboard-page.ts", "status")},
		"urlParams":                  {producer: rootMarker("internal/dashboard/ui/page.go", "urlParams"), reader: rootMarker("web/components/dashboard/dashboard-page.ts", "urlParams")},
		"visualWindowCommand":        {producer: rootMarker("internal/dashboard/ui/page.go", "visualWindowCommand"), allow: "surface-specific"},
		"visuals":                    {producer: rootMarker("internal/dashboard/ui/page.go", "visuals"), reader: rootMarker("web/components/dashboard/dashboard-page.ts", "visuals")},
	}
}

func declaredSignalRoots(doc ir.Document) map[string]struct{} {
	roots := make(map[string]struct{})
	for _, schema := range doc.Schemas {
		for _, property := range schema.Properties {
			if root, ok := property.Extensions["x-leapview-signal-key"].(string); ok && root != "" {
				roots[root] = struct{}{}
			}
		}
	}
	return roots
}

func declaredSignalRootOwners(doc ir.Document) map[string]string {
	owners := make(map[string]string)
	for _, schema := range doc.Schemas {
		for _, property := range schema.Properties {
			root, ok := property.Extensions["x-leapview-signal-key"].(string)
			if !ok || root == "" {
				continue
			}
			if owner, ok := property.Extensions["x-leapview-signal-owner"].(string); ok && owner != "" {
				owners[root] = owner
			}
		}
	}
	return owners
}

func validateSignalRootEvidence(repository string, doc ir.Document, evidence map[string]signalRootEvidence) error {
	declared := declaredSignalRoots(doc)
	owners := declaredSignalRootOwners(doc)
	for _, root := range sortedSignalRoots(declared) {
		entry, ok := evidence[root]
		if !ok {
			return fmt.Errorf("declared signal root %q has no producer/reader evidence", root)
		}
		if entry.producer.path == "" && entry.reader.path == "" {
			return fmt.Errorf("declared signal root %q has empty evidence", root)
		}
		if entry.allow == "" && (entry.producer.path == "" || entry.reader.path == "") {
			return fmt.Errorf("declared signal root %q needs both producer and reader evidence", root)
		}
		if owner := owners[root]; owner != "" {
			if entry.allow != "" && entry.allow != owner {
				return fmt.Errorf("declared signal root %q owner %q disagrees with evidence exception %q", root, owner, entry.allow)
			}
			switch owner {
			case "server":
				if entry.producer.path == "" {
					return fmt.Errorf("server-owned signal root %q has no producer evidence", root)
				}
			case "browser", "consumer-only":
				if entry.producer.path != "" {
					return fmt.Errorf("%s signal root %q must not name a server producer", owner, root)
				}
			}
		}
		for direction, marker := range map[string]sourceMarker{"producer": entry.producer, "reader": entry.reader} {
			if marker.path == "" {
				continue
			}
			if repository == "" {
				continue
			}
			content, err := os.ReadFile(filepath.Join(repository, marker.path))
			if err != nil {
				return fmt.Errorf("%s evidence for %q: %w", direction, root, err)
			}
			if !strings.Contains(string(content), marker.marker) {
				return fmt.Errorf("%s evidence for %q is stale: %s lacks %q", direction, root, marker.path, marker.marker)
			}
		}
	}
	for _, root := range sortedSignalRoots(evidenceRoots(evidence)) {
		if _, ok := declared[root]; !ok {
			return fmt.Errorf("live signal root %q is not declared in the TypeSpec IR", root)
		}
	}
	return nil
}

var signalCallPattern = regexp.MustCompile(`signal(?:<[^>\n]+>)?\(\s*['"]([^'"]+)['"]`)

func liveSignalRootsFromTypeScript(repository string) (map[string]struct{}, error) {
	roots := make(map[string]struct{})
	err := filepath.WalkDir(filepath.Join(repository, "web"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ts") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range signalCallPattern.FindAllStringSubmatch(string(content), -1) {
			root := match[1]
			if strings.HasPrefix(root, "dashboard.") || root == "tables" {
				continue // component-local benchmark/test paths, not stream roots.
			}
			root = strings.SplitN(root, ".", 2)[0]
			roots[root] = struct{}{}
		}
		return nil
	})
	return roots, err
}

func TestDeclaredSignalRootsHaveSourceEvidence(t *testing.T) {
	root := repositoryRoot()
	doc, err := ir.Load(filepath.Join(root, "api/gen/ui-signals-ir.json"))
	if err != nil {
		t.Fatal(err)
	}
	evidence := signalRootEvidenceCatalog()
	if err := validateSignalRootEvidence(root, doc, evidence); err != nil {
		t.Fatal(err)
	}
	live, err := liveSignalRootsFromTypeScript(root)
	if err != nil {
		t.Fatal(err)
	}
	declared := declaredSignalRoots(doc)
	for _, root := range sortedSignalRoots(live) {
		if _, ok := declared[root]; !ok {
			t.Errorf("live TypeScript signal root %q is not declared in the TypeSpec IR", root)
		}
	}
}

func TestSignalRootEvidenceRejectsUndeclaredFixture(t *testing.T) {
	doc := fixtureSignalDocument("declared")
	evidence := map[string]signalRootEvidence{
		"declared": {producer: sourceMarker{path: "producer", marker: "declared"}, reader: sourceMarker{path: "reader", marker: "declared"}},
		"rogue":    {reader: sourceMarker{path: "reader", marker: "rogue"}, allow: "fixture"},
	}
	if err := validateSignalRootEvidence("", doc, evidence); err == nil || !strings.Contains(err.Error(), `live signal root "rogue" is not declared`) {
		t.Fatalf("undeclared fixture error = %v", err)
	}
}

func TestSignalRootEvidenceRejectsUnconsumedFixture(t *testing.T) {
	doc := fixtureSignalDocument("declared", "unconsumed")
	evidence := map[string]signalRootEvidence{
		"declared": {producer: sourceMarker{path: "producer", marker: "declared"}, reader: sourceMarker{path: "reader", marker: "declared"}},
	}
	if err := validateSignalRootEvidence("", doc, evidence); err == nil || !strings.Contains(err.Error(), `declared signal root "unconsumed" has no producer/reader evidence`) {
		t.Fatalf("unconsumed fixture error = %v", err)
	}
}

func fixtureSignalDocument(roots ...string) ir.Document {
	properties := make(map[string]ir.SchemaProperty, len(roots))
	for _, root := range roots {
		properties[root] = ir.SchemaProperty{Extensions: map[string]any{"x-leapview-signal-key": root}}
	}
	return ir.Document{Schemas: map[string]ir.Schema{"FixtureEnvelope": {Properties: properties}}}
}

func evidenceRoots(evidence map[string]signalRootEvidence) map[string]struct{} {
	roots := make(map[string]struct{}, len(evidence))
	for root := range evidence {
		roots[root] = struct{}{}
	}
	return roots
}

func sortedSignalRoots(roots map[string]struct{}) []string {
	values := make([]string, 0, len(roots))
	for root := range roots {
		values = append(values, root)
	}
	sort.Strings(values)
	return values
}
