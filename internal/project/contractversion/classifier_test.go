package contractversion

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestClassifyCompatibleAdditiveAndBehavioralChanges(t *testing.T) {
	base := sourceDocument("1.0.0", `"id":{"datatype":"String","nullable":false}`)
	additive := sourceDocument("1.1.0", `"id":{"datatype":"String","nullable":false},"note":{"datatype":"String","nullable":true}`)
	result, err := Classify(base, additive)
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != Compatible || result.Compatibility != CompatibilityAdditive || result.StructuralCompatibility != CompatibilityAdditive || result.SemanticCompatibility != CompatibilityAdditive || result.SecurityImpact != SecurityNone || result.RequiresMajor {
		t.Fatalf("additive classification = %#v", result)
	}
	if len(result.Changes) != 1 || result.Changes[0].Path != "contract.schema.fields.note" {
		t.Fatalf("additive changes = %#v", result.Changes)
	}

	behavioral := sourceDocument("1.1.0", `"id":{"criticalDataElement":true,"datatype":"String","nullable":false}`)
	result, err = Classify(base, behavioral)
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != Warning || result.Compatibility != CompatibilityBehavioral || result.StructuralCompatibility != CompatibilityBehavioral || result.SecurityImpact != SecurityNone || result.RequiresMajor {
		t.Fatalf("behavioral classification = %#v", result)
	}
}

func TestClassifyBreakingChange(t *testing.T) {
	base := sourceDocument("1.0.0", `"id":{"datatype":"String","nullable":false}`)
	candidate := sourceDocument("2.0.0", `"id":{"datatype":"Integer","nullable":false}`)
	result, err := Classify(base, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != Breaking || result.Compatibility != CompatibilityBreaking || result.StructuralCompatibility != CompatibilityBreaking || !result.RequiresMajor || result.SecurityImpact != SecurityNone {
		t.Fatalf("breaking classification = %#v", result)
	}
}

func TestClassifyCurrentModelProjectionShape(t *testing.T) {
	base := modelDocument("1.0.0", `"id":{"datatype":"String","nullable":false}`, "")
	candidate := modelDocument("2.0.0", `"id":{"datatype":"Integer","nullable":false}`, `[{"id":"id_present","severity":"warning","type":"non_null"}]`)
	result, err := Classify(base, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != Breaking || result.Compatibility != CompatibilityBreaking || result.StructuralCompatibility != CompatibilityBreaking || result.SemanticCompatibility != CompatibilityBehavioral || !result.RequiresMajor || len(result.Changes) != 2 {
		t.Fatalf("model projection classification = %#v", result)
	}
	if result.Changes[0].Path != "contract.checks" || result.Changes[1].Path != "contract.fields.id.datatype" {
		t.Fatalf("model projection paths = %#v", result.Changes)
	}
}

func TestClassifySecurityTighteningWideningMixedAndIndeterminate(t *testing.T) {
	base := semanticDocument("1.0.0", false, "region", `[{"type":"String","value":"emea"}]`)
	tightened := semanticDocument("2.0.0", true, "region", `[{"type":"String","value":"emea"}]`)
	result, err := Classify(base, tightened)
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != SecuritySensitive || result.Compatibility != CompatibilityBreaking || result.SecurityImpact != SecurityTightening || !result.RequiresMajor || result.RequiresSecurityApproval {
		t.Fatalf("tightening classification = %#v", result)
	}

	widenBase := semanticDocument("1.0.0", true, "region", `[{"type":"String","value":"emea"}]`)
	widened := semanticDocument("1.1.0", false, "region", `[{"type":"String","value":"emea"}]`)
	result, err = Classify(widenBase, widened)
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != SecuritySensitive || result.Compatibility != CompatibilityBehavioral || result.SecurityImpact != SecurityWidening || result.RequiresMajor || !result.RequiresSecurityApproval {
		t.Fatalf("widening classification = %#v", result)
	}

	// The candidate both adds a required grant (tightening) and expands the
	// allowed values (widening). The aggregate must fail closed as indeterminate.
	mixed := semanticDocument("2.0.0", true, "region", `[{"type":"String","value":"amer"},{"type":"String","value":"emea"}]`)
	result, err = Classify(base, mixed)
	if err != nil {
		t.Fatal(err)
	}
	if result.SecurityImpact != SecurityIndeterminate || result.Compatibility != CompatibilityBreaking || !result.RequiresMajor || !result.RequiresSecurityApproval {
		t.Fatalf("mixed classification = %#v", result)
	}
	if err := result.ValidatePublication(); !errors.Is(err, ErrIndeterminate) {
		t.Fatalf("mixed publication validation = %v", err)
	}

	indeterminate := semanticDocument("2.0.0", false, "country", `[{"type":"String","value":"emea"}]`)
	result, err = Classify(base, indeterminate)
	if err != nil {
		t.Fatal(err)
	}
	if result.SecurityImpact != SecurityIndeterminate || result.Compatibility != CompatibilityBreaking || !result.RequiresMajor || !result.RequiresSecurityApproval {
		t.Fatalf("indeterminate classification = %#v", result)
	}
}

func TestClassifyDeterministicOrderingAndIdentity(t *testing.T) {
	base := sourceDocument("1.0.0", `"id":{"datatype":"String","nullable":false}`)
	candidate := sourceDocument("2.0.0", `"id":{"datatype":"Integer","nullable":false},"note":{"datatype":"String","nullable":true}`)
	first, err := Classify(base, candidate)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Classify(base, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%#v", first) != fmt.Sprintf("%#v", second) {
		t.Fatalf("classification is not deterministic: first=%#v second=%#v", first, second)
	}
	if len(first.Changes) != 2 || first.Changes[0].Path != "contract.schema.fields.id.datatype" || first.Changes[1].Path != "contract.schema.fields.note" {
		t.Fatalf("changes are not path sorted: %#v", first.Changes)
	}

	identity := sourceDocumentWithID("1.0.0", "source:customers", `"id":{"datatype":"String","nullable":false}`)
	if _, err := Classify(base, identity); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("identity mismatch error = %v", err)
	}
}

func TestValidateVersionTransitionAndReuse(t *testing.T) {
	base := sourceDocument("1.0.0", `"id":{"datatype":"String","nullable":false}`)
	additive := sourceDocument("1.1.0", `"id":{"datatype":"String","nullable":false},"note":{"datatype":"String","nullable":true}`)
	if _, err := ValidateVersionTransition(base, sourceDocument("1.0.1", `"id":{"datatype":"String","nullable":false},"note":{"datatype":"String","nullable":true}`)); !errors.Is(err, ErrVersionPolicy) {
		t.Fatalf("patch additive transition error = %v", err)
	}
	if _, err := ValidateVersionTransition(base, additive); err != nil {
		t.Fatalf("minor additive transition error = %v", err)
	}
	breaking := sourceDocument("1.1.0", `"id":{"datatype":"Integer","nullable":false}`)
	if _, err := ValidateVersionTransition(base, breaking); !errors.Is(err, ErrVersionPolicy) {
		t.Fatalf("same-major breaking transition error = %v", err)
	}
	if _, err := ValidateVersionTransition(base, sourceDocument("2.0.0", `"id":{"datatype":"Integer","nullable":false}`)); err != nil {
		t.Fatalf("major breaking transition error = %v", err)
	}

	if _, err := ValidateVersionTransition(base, additiveWithVersionAndFields("1.0.0+build.1", `"id":{"datatype":"String","nullable":false},"note":{"datatype":"String","nullable":true}`)); !errors.Is(err, ErrVersionReuseConflict) {
		t.Fatalf("version reuse error = %v", err)
	}
	if baseline, err := SemverBaseline("1.2.3+build.4"); err != nil || baseline != "v1.2.3" {
		t.Fatalf("semver baseline = %q, %v", baseline, err)
	}
}

func TestClassifyInitialPublication(t *testing.T) {
	candidate := sourceDocument("1.0.0", `"id":{"datatype":"String","nullable":false},"note":{"datatype":"String","nullable":true}`)
	result, err := ClassifyInitial(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != Breaking || result.Compatibility != CompatibilityBreaking || result.StructuralCompatibility != CompatibilityBreaking || result.SemanticCompatibility != CompatibilityAdditive || !result.RequiresMajor || len(result.Changes) != 3 {
		t.Fatalf("initial classification = %#v", result)
	}
}

func TestClassifyInitialMinimalSemanticModelDoesNotInventRemovals(t *testing.T) {
	candidate := []byte(`{"apiVersion":"leapview.dev/v1","contract":{"datasets":{},"metrics":{}},"kind":"SemanticModel","metadata":{"contract":{"compatibility":"backward","version":"1.0.0"},"id":"semantic-model:empty","name":"empty"},"profile":"leapview.contract/v1"}`)
	result, err := ClassifyInitial(candidate)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range result.Changes {
		if change.Operation == OperationRemoved {
			t.Fatalf("genesis classification invented removal: %#v", change)
		}
	}
}

func TestClassifyRejectsNonCanonicalAndUnknownProjectionBytes(t *testing.T) {
	valid := sourceDocument("1.0.0", `"id":{"datatype":"String","nullable":false}`)
	for name, candidate := range map[string][]byte{
		"leading whitespace": append([]byte(" "), valid...),
		"unknown member":     []byte(strings.Replace(string(valid), `{"apiVersion":`, `{"unknown":true,"apiVersion":`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Classify(valid, candidate); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("error = %v, want ErrInvalidContract", err)
			}
		})
	}
}

func TestResultValidateRejectsForgedSecurityImpact(t *testing.T) {
	result := aggregate([]Change{{
		Path: "contract.schema.fields.note", Operation: OperationAdded,
		Domain: DomainStructural, Class: SecuritySensitive,
		Compatibility: CompatibilityAdditive, SecurityImpact: SecurityWidening,
		Reason: "forged widening",
	}})
	if err := result.Validate(); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("forged structural security result error = %v", err)
	}
}

func TestClassifyRejectsMalformedAndUnsupportedInputs(t *testing.T) {
	valid := sourceDocument("1.0.0", `"id":{"datatype":"String","nullable":false}`)
	for name, candidate := range map[string][]byte{
		"empty":       nil,
		"malformed":   []byte(`{"profile":`),
		"trailing":    append(valid, []byte(` {}`)...),
		"unsupported": []byte(strings.Replace(string(valid), `"kind":"Source"`, `"kind":"Connection"`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Classify(valid, candidate); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("error = %v, want ErrInvalidContract", err)
			}
		})
	}
}

func sourceDocument(version, fields string) []byte {
	return sourceDocumentWithID(version, "source:orders", fields)
}

func sourceDocumentWithID(version, id, fields string) []byte {
	return []byte(fmt.Sprintf(`{"apiVersion":"leapview.dev/v1","contract":{"schema":{"fields":{%s},"mode":"strict"}},"kind":"Source","metadata":{"contract":{"compatibility":"backward","version":"%s"},"id":"%s","name":"orders"},"profile":"leapview.contract/v1"}`, fields, version, id))
}

func modelDocument(version, fields, checks string) []byte {
	checksPrefix := ""
	if checks != "" {
		checksPrefix = fmt.Sprintf(`"checks":%s,`, checks)
	}
	return []byte(fmt.Sprintf(`{"apiVersion":"leapview.dev/v1","contract":{%s"definition":{"source":"source:orders","type":"direct"},"entities":{},"fields":{%s},"grain":{"entity":"order"}},"kind":"Model","metadata":{"contract":{"compatibility":"backward","version":"%s"},"id":"model:orders","name":"orders"},"profile":"leapview.contract/v1"}`, checksPrefix, fields, version))
}

func additiveWithVersionAndFields(version, fields string) []byte {
	return sourceDocumentWithID(version, "source:orders", fields)
}

func semanticDocument(version string, required bool, userAttribute, allowed string) []byte {
	requiredGrant := ""
	if required {
		requiredGrant = `,"requiredAccessGrants":["region_access"]`
	}
	return []byte(fmt.Sprintf(`{"apiVersion":"leapview.dev/v1","contract":{"accessGrants":{"region_access":{"allowedValues":%s,"userAttribute":"%s"}},"datasets":{"orders":{"model":"model:orders"%s}},"metrics":{"order_count":{"aggregation":"count","dataset":"orders","empty":"zero","input":{"field":"orders.id"},"type":"aggregate"}}},"kind":"SemanticModel","metadata":{"contract":{"compatibility":"backward","version":"%s"},"id":"semantic-model:sales","name":"sales"},"profile":"leapview.contract/v1"}`, allowed, userAttribute, requiredGrant, version))
}
