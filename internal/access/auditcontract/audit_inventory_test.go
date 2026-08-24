package auditcontract

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type inventory struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Guarantees    map[string]string   `json:"guarantees"`
	Producers     []inventoryProducer `json:"producers"`
}

type inventoryProducer struct {
	ID                   string              `json:"id"`
	Owner                string              `json:"owner"`
	Guarantee            string              `json:"guarantee"`
	CompletionGuarantee  string              `json:"completionGuarantee"`
	ImplementationStatus string              `json:"implementationStatus"`
	FailureBehavior      string              `json:"failureBehavior"`
	Implementation       []string            `json:"implementation"`
	ContractSources      []string            `json:"contractSources"`
	Evidence             []string            `json:"evidence"`
	Members              map[string][]string `json:"members"`
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func loadInventory(t *testing.T) (inventory, string) {
	t.Helper()
	root := repositoryRoot(t)
	path := filepath.Join(root, "adr", "specifications", "durable-audit-inventory.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	var value inventory
	if err := json.Unmarshal(contents, &value); err != nil {
		t.Fatalf("decode inventory: %v", err)
	}
	return value, root
}

func TestDurableAuditInventoryIsComplete(t *testing.T) {
	value, root := loadInventory(t)
	if value.SchemaVersion != 1 {
		t.Fatalf("schema version = %d, want 1", value.SchemaVersion)
	}
	allowed := map[string]bool{
		"transactional":         true,
		"durable-before-stream": true,
		"best-effort":           true,
		"mixed":                 true,
	}
	for guarantee, description := range value.Guarantees {
		if !allowed[guarantee] {
			t.Errorf("guarantee %q is not a supported class", guarantee)
		}
		if strings.TrimSpace(description) == "" {
			t.Errorf("guarantee %q has no description", guarantee)
		}
	}
	for _, required := range []string{"transactional", "durable-before-stream", "best-effort", "mixed"} {
		if strings.TrimSpace(value.Guarantees[required]) == "" {
			t.Errorf("inventory does not define guarantee %q", required)
		}
	}

	ids := make(map[string]bool, len(value.Producers))
	owners := make(map[string][]string)
	for _, producer := range value.Producers {
		if strings.TrimSpace(producer.ID) == "" {
			t.Error("producer has no id")
		}
		if ids[producer.ID] {
			t.Errorf("duplicate producer id %q", producer.ID)
		}
		ids[producer.ID] = true
		if strings.TrimSpace(producer.Owner) == "" || strings.TrimSpace(producer.FailureBehavior) == "" {
			t.Errorf("producer %q lacks owner or failure behavior", producer.ID)
		}
		if !allowed[producer.Guarantee] {
			t.Errorf("producer %q has unclassified guarantee %q", producer.ID, producer.Guarantee)
		}
		if producer.Guarantee == "durable-before-stream" && producer.CompletionGuarantee != "best-effort" {
			t.Errorf("producer %q must classify its completion guarantee", producer.ID)
		}
		if producer.Guarantee == "mixed" {
			for _, required := range []string{"transactional", "best-effort"} {
				if len(producer.Members[required]) == 0 {
					t.Errorf("mixed producer %q has no %s members", producer.ID, required)
				}
			}
		}
		if len(producer.Implementation) == 0 && len(producer.ContractSources) == 0 {
			t.Errorf("producer %q has no source", producer.ID)
		}
		for _, source := range append(append([]string{}, producer.Implementation...), producer.ContractSources...) {
			if _, err := os.Stat(filepath.Join(root, source)); err != nil {
				t.Errorf("producer %q source %q: %v", producer.ID, source, err)
			}
			owners[source] = append(owners[source], producer.ID)
		}
		for _, evidence := range producer.Evidence {
			if _, err := os.Stat(filepath.Join(root, evidence)); err != nil {
				t.Errorf("producer %q evidence %q: %v", producer.ID, evidence, err)
			}
		}
	}

	// A callback is the stable implementation marker for a best-effort audit.
	// Any production callback added later must name an inventory owner in the
	// same change; otherwise this test fails before the producer can ship.
	var unclassified []string
	if err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !strings.Contains(string(contents), "BestEffortAudit:") {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if len(owners[filepath.ToSlash(relative)]) == 0 {
			unclassified = append(unclassified, filepath.ToSlash(relative))
		}
		return nil
	}); err != nil {
		t.Fatalf("scan production audit callbacks: %v", err)
	}
	if len(unclassified) != 0 {
		sort.Strings(unclassified)
		t.Fatalf("best-effort audit callbacks without inventory owner: %s", strings.Join(unclassified, ", "))
	}

	// Generated command guarantees are authored in TypeSpec. Scan every source
	// declaration rather than relying on a hand-maintained list of filenames.
	// This must include transactional-only contracts as well as best-effort
	// contracts so adding a command source cannot bypass inventory ownership.
	var unclassifiedContracts []string
	if err := filepath.WalkDir(filepath.Join(root, "api", "typespec"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".tsp") {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !strings.Contains(string(contents), "@apigen.command") {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if len(owners[filepath.ToSlash(relative)]) == 0 {
			unclassifiedContracts = append(unclassifiedContracts, filepath.ToSlash(relative))
		}
		return nil
	}); err != nil {
		t.Fatalf("scan TypeSpec audit contracts: %v", err)
	}
	if len(unclassifiedContracts) != 0 {
		sort.Strings(unclassifiedContracts)
		t.Fatalf("TypeSpec command contracts without inventory owner: %s", strings.Join(unclassifiedContracts, ", "))
	}
}

func TestDurableAuditInventoryHasStableSourcePaths(t *testing.T) {
	value, _ := loadInventory(t)
	for _, producer := range value.Producers {
		for _, source := range append(append([]string{}, producer.Implementation...), producer.ContractSources...) {
			if filepath.IsAbs(source) || strings.Contains(source, "\\") {
				t.Errorf("producer %q source %q is not a repository-relative path", producer.ID, source)
			}
		}
	}
	if len(value.Producers) < 10 {
		t.Fatalf("inventory has %d producers; expected at least 10 security-relevant producers", len(value.Producers))
	}
}

func Example_inventoryGuarantee() {
	value := inventory{Guarantees: map[string]string{"best-effort": "bounded"}}
	fmt.Println(value.Guarantees["best-effort"])
	// Output: bounded
}
