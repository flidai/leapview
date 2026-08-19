// Package contractimport resolves schemas owned by imported TypeSpec packages.
package contractimport

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Yacobolo/toolbelt/apigen/ir"
)

// Binding maps one TypeSpec namespace to generated language imports.
type Binding struct {
	GoPackage        string
	GoAlias          string
	TypeScriptModule string
	// ExactNamespace prevents a package-plan dependency on a root namespace
	// from claiming schemas in capability child namespaces. Authored external
	// contract imports retain prefix matching.
	ExactNamespace bool
}

// Bindings maps fully-qualified TypeSpec namespaces to language imports.
type Bindings map[string]Binding

// Resolve returns the longest matching external namespace binding.
func (bindings Bindings) Resolve(namespace string) (string, Binding, bool) {
	namespace = strings.TrimSpace(namespace)
	best := ""
	var resolved Binding
	for candidate, binding := range bindings {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" ||
			(namespace != candidate && (binding.ExactNamespace || !strings.HasPrefix(namespace, candidate+"."))) {
			continue
		}
		if len(candidate) > len(best) {
			best, resolved = candidate, binding
		}
	}
	return best, resolved, best != ""
}

// Schema resolves an imported schema by its IR name.
func (bindings Bindings) Schema(doc ir.Document, name string) (string, Binding, bool) {
	schema, ok := doc.Schemas[name]
	if !ok {
		return "", Binding{}, false
	}
	return bindings.Resolve(schema.Namespace)
}

// Validate requires every non-local named namespace to have one unambiguous
// import and validates language-specific aliases.
func (bindings Bindings) Validate(doc ir.Document) error {
	type bindingOwner struct {
		namespace string
		binding   Binding
	}
	aliases := map[string]bindingOwner{}
	packages := map[string]bindingOwner{}
	for namespace, binding := range bindings {
		if strings.TrimSpace(namespace) == "" {
			return fmt.Errorf("contract import namespace is required")
		}
		if binding.GoPackage != "" && binding.GoAlias == "" {
			return fmt.Errorf("contract import %q requires go_alias when go_package is set", namespace)
		}
		if previous, exists := aliases[binding.GoAlias]; binding.GoAlias != "" && exists &&
			previous.binding.GoPackage != binding.GoPackage {
			return fmt.Errorf("contract imports %q and %q share Go alias %q", previous.namespace, namespace, binding.GoAlias)
		}
		if binding.GoAlias != "" {
			if binding.GoAlias == "bytes" || binding.GoAlias == "json" || binding.GoAlias == "fmt" {
				return fmt.Errorf("contract import %q uses reserved Go alias %q", namespace, binding.GoAlias)
			}
			aliases[binding.GoAlias] = bindingOwner{namespace: namespace, binding: binding}
		}
		if previous, exists := packages[binding.GoPackage]; binding.GoPackage != "" && exists &&
			previous.binding != binding {
			return fmt.Errorf("contract imports %q and %q share Go package %q with inconsistent bindings", previous.namespace, namespace, binding.GoPackage)
		}
		if binding.GoPackage != "" {
			packages[binding.GoPackage] = bindingOwner{namespace: namespace, binding: binding}
		}
	}
	local := strings.TrimSpace(doc.Info.Namespace)
	for name, schema := range doc.Schemas {
		namespace := strings.TrimSpace(schema.Namespace)
		if namespace == "" || namespace == local || (local != "" && strings.HasPrefix(namespace, local+".")) {
			continue
		}
		if _, _, ok := bindings.Resolve(namespace); !ok {
			return fmt.Errorf("schema %q belongs to external namespace %q without a contract_imports mapping", name, namespace)
		}
	}
	for name, schema := range doc.Schemas {
		nameExternal := bindings.isExternal(doc, name)
		for _, dependency := range schemaDependencies(schema) {
			if dependency == "Record" {
				continue
			}
			if _, exists := doc.Schemas[dependency]; !exists {
				return fmt.Errorf("schema %q references unavailable exported model %q", name, dependency)
			}
			dependencyExternal := bindings.isExternal(doc, dependency)
			if nameExternal && !dependencyExternal {
				return fmt.Errorf("contract import cycle: external schema %q references local schema %q", name, dependency)
			}
			if schema.Type == "union" && schema.Discriminator != nil && !nameExternal && dependencyExternal {
				return fmt.Errorf("local union %q cannot use imported variant %q", name, dependency)
			}
		}
	}
	return nil
}

func (bindings Bindings) isExternal(doc ir.Document, name string) bool {
	_, _, ok := bindings.Schema(doc, name)
	return ok
}

func schemaDependencies(schema ir.Schema) []string {
	seen := map[string]struct{}{}
	var visit func(ir.SchemaRef)
	visit = func(ref ir.SchemaRef) {
		if name, ok := ir.NormalizedSchemaRefName(ref); ok {
			seen[name] = struct{}{}
		}
		if ref.Items != nil {
			visit(*ref.Items)
		}
		if ref.PropertyNames != nil {
			visit(*ref.PropertyNames)
		}
		if ref.AdditionalProperties != nil && ref.AdditionalProperties.Schema != nil {
			visit(*ref.AdditionalProperties.Schema)
		}
	}
	for _, property := range schema.Properties {
		visit(property.Schema)
	}
	if schema.Items != nil {
		visit(*schema.Items)
	}
	if schema.Base != nil {
		visit(*schema.Base)
	}
	for _, variant := range schema.OneOf {
		visit(variant)
	}
	values := make([]string, 0, len(seen))
	for name := range seen {
		values = append(values, name)
	}
	sort.Strings(values)
	return values
}

// Namespaces returns configured namespaces in stable order.
func (bindings Bindings) Namespaces() []string {
	values := make([]string, 0, len(bindings))
	for namespace := range bindings {
		values = append(values, namespace)
	}
	sort.Strings(values)
	return values
}
