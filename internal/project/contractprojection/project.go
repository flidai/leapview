package contractprojection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	configschema "github.com/flidai/leapview/internal/project/schema"
)

type authoredMetadata struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Contract *authoredContract `json:"contract,omitempty"`
}

type authoredContract struct {
	Version       string `json:"version"`
	Compatibility string `json:"compatibility"`
}

type authoredField struct {
	Datatype                 *string               `json:"datatype,omitempty"`
	Nullable                 *bool                 `json:"nullable,omitempty"`
	CriticalDataElement      *bool                 `json:"criticalDataElement,omitempty"`
	Classification           *string               `json:"classification,omitempty"`
	AuthoritativeDefinitions *[]authoredDefinition `json:"authoritativeDefinitions,omitempty"`
	Deprecation              *authoredDeprecation  `json:"deprecation,omitempty"`
}

type authoredDefinition struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type authoredDeprecation struct {
	Since       string  `json:"since"`
	Reason      string  `json:"reason"`
	Replacement *string `json:"replacement,omitempty"`
}

type authoredDuration struct {
	Amount int64  `json:"amount"`
	Unit   string `json:"unit"`
}

func ProjectSource(value projectcontracts.Source, contract Contract) (Source, error) {
	if err := validateAuthoredResource(configschema.KindSource, value); err != nil {
		return Source{}, fmt.Errorf("project Source: validate authored resource: %w", err)
	}
	var input struct {
		APIVersion string           `json:"apiVersion"`
		Kind       string           `json:"kind"`
		Metadata   authoredMetadata `json:"metadata"`
		Spec       struct {
			Schema    json.RawMessage `json:"schema,omitempty"`
			Freshness json.RawMessage `json:"freshness,omitempty"`
		} `json:"spec"`
	}
	if err := decodeGenerated(value, &input); err != nil {
		return Source{}, fmt.Errorf("project Source: %w", err)
	}
	metadata, err := projectMetadata(input.APIVersion, input.Kind, "Source", input.Metadata, contract)
	if err != nil {
		return Source{}, err
	}
	schema, err := projectSourceSchema(input.Spec.Schema)
	if err != nil {
		return Source{}, err
	}
	freshness, err := projectSourceFreshness(input.Spec.Freshness)
	if err != nil {
		return Source{}, err
	}
	return Source{payload: projectcontracts.SourceContractProjection{Profile: Profile, APIVersion: input.APIVersion, Kind: input.Kind, Metadata: metadata, Contract: SourceContract{Schema: schema, Freshness: freshness}}}, nil
}

func projectSourceSchema(raw json.RawMessage) (SourceSchema, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return SourceSchema{Mode: "inferred"}, nil
	}
	var input struct {
		Mode   string                   `json:"mode"`
		Fields map[string]authoredField `json:"fields,omitempty"`
	}
	if err := decodeJSON(raw, &input); err != nil {
		return SourceSchema{}, fmt.Errorf("project Source schema: %w", err)
	}
	mode, err := canonicalText(input.Mode)
	if err != nil {
		return SourceSchema{}, fmt.Errorf("project Source schema mode: %w", err)
	}
	result := SourceSchema{Mode: mode}
	if len(input.Fields) > 0 {
		fields := make(map[string]Field, len(input.Fields))
		for name, value := range input.Fields {
			field, err := projectField(value)
			if err != nil {
				return SourceSchema{}, fmt.Errorf("project Source field %q: %w", name, err)
			}
			fields[name] = field
		}
		result.Fields = &fields
	}
	return result, nil
}

func projectSourceFreshness(raw json.RawMessage) (*SourceFreshness, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var input struct {
		Basis        string            `json:"basis"`
		Field        *string           `json:"field,omitempty"`
		Revision     *string           `json:"revision,omitempty"`
		WarningAfter *authoredDuration `json:"warningAfter,omitempty"`
		ErrorAfter   *authoredDuration `json:"errorAfter,omitempty"`
	}
	if err := decodeJSON(raw, &input); err != nil {
		return nil, fmt.Errorf("project Source freshness: %w", err)
	}
	result := &SourceFreshness{Basis: input.Basis, WarningAfter: projectDuration(input.WarningAfter), ErrorAfter: projectDuration(input.ErrorAfter)}
	var err error
	result.Basis, err = canonicalText(result.Basis)
	if err != nil {
		return nil, err
	}
	if input.Field != nil {
		result.Field, err = canonicalTextPointer(input.Field)
		if err != nil {
			return nil, err
		}
	}
	if input.Revision != nil {
		parsed, parseErr := time.Parse(time.RFC3339Nano, *input.Revision)
		if parseErr != nil {
			return nil, fmt.Errorf("project Source freshness revision: %w", parseErr)
		}
		revision := parsed.UTC().Format(time.RFC3339Nano)
		result.Revision = &revision
	}
	return result, nil
}

func ProjectModel(value projectcontracts.Model, contract Contract, contexts ...ReferenceContext) (Model, error) {
	resolver, err := referenceContextArgument(contexts)
	if err != nil {
		return Model{}, err
	}
	if err := validateAuthoredResource(configschema.KindModel, value); err != nil {
		return Model{}, fmt.Errorf("project Model: validate authored resource: %w", err)
	}
	var input struct {
		APIVersion string           `json:"apiVersion"`
		Kind       string           `json:"kind"`
		Metadata   authoredMetadata `json:"metadata"`
		Spec       struct {
			Definition struct {
				Type   string  `json:"type"`
				Source *string `json:"source,omitempty"`
				SQL    *string `json:"sql,omitempty"`
			} `json:"definition"`
			Entities map[string]struct {
				Type   string   `json:"type"`
				Fields []string `json:"fields"`
			} `json:"entities"`
			Grain struct {
				Entity string `json:"entity"`
			} `json:"grain"`
			Fields map[string]authoredField `json:"fields,omitempty"`
			Checks []struct {
				ID       string   `json:"id"`
				Type     string   `json:"type"`
				Field    *string  `json:"field,omitempty"`
				Fields   []string `json:"fields,omitempty"`
				Values   []string `json:"values,omitempty"`
				To       *string  `json:"to,omitempty"`
				Minimum  *int64   `json:"minimum,omitempty"`
				Maximum  *int64   `json:"maximum,omitempty"`
				Severity *string  `json:"severity,omitempty"`
			} `json:"checks,omitempty"`
		} `json:"spec"`
	}
	if err := decodeGenerated(value, &input); err != nil {
		return Model{}, fmt.Errorf("project Model: %w", err)
	}
	metadata, err := projectMetadata(input.APIVersion, input.Kind, "Model", input.Metadata, contract)
	if err != nil {
		return Model{}, err
	}
	definition := ModelDefinition{Type: input.Spec.Definition.Type}
	definition.Type, err = canonicalText(definition.Type)
	if err != nil {
		return Model{}, err
	}
	definition.Source, err = canonicalTextPointer(input.Spec.Definition.Source)
	if err != nil {
		return Model{}, err
	}
	if definition.Source != nil {
		resolved, resolveErr := resolveProjectionReference(resolver, *definition.Source, projectgraph.KindSource, "Model definition source")
		if resolveErr != nil {
			return Model{}, resolveErr
		}
		definition.Source = &resolved
	}
	if input.Spec.Definition.SQL != nil {
		definition.SQLAst, err = canonicalModelSQL(*input.Spec.Definition.SQL, resolver)
		if err != nil {
			return Model{}, fmt.Errorf("project Model SQL: %w", err)
		}
	}
	entities := make(map[string]ModelEntity, len(input.Spec.Entities))
	for name, value := range input.Spec.Entities {
		kind, err := canonicalText(value.Type)
		if err != nil {
			return Model{}, fmt.Errorf("project Model entity %q: %w", name, err)
		}
		fields, err := canonicalTexts(value.Fields)
		if err != nil {
			return Model{}, fmt.Errorf("project Model entity %q: %w", name, err)
		}
		entities[name] = ModelEntity{Type: kind, Fields: fields}
	}
	fields := make(map[string]ModelField, len(input.Spec.Fields))
	for name, value := range input.Spec.Fields {
		field, err := projectModelField(value)
		if err != nil {
			return Model{}, fmt.Errorf("project Model field %q: %w", name, err)
		}
		fields[name] = field
	}
	checks := make([]ModelCheck, len(input.Spec.Checks))
	for index, value := range input.Spec.Checks {
		check := ModelCheck{Minimum: value.Minimum, Maximum: value.Maximum}
		var err error
		check.ID, err = canonicalText(value.ID)
		if err != nil {
			return Model{}, err
		}
		check.Type, err = canonicalText(value.Type)
		if err != nil {
			return Model{}, err
		}
		check.Field, err = canonicalTextPointer(value.Field)
		if err != nil {
			return Model{}, err
		}
		check.To, err = canonicalTextPointer(value.To)
		if err != nil {
			return Model{}, err
		}
		if check.To != nil {
			resolved, resolveErr := resolveModelMemberReference(resolver, *check.To, "Model relationship check target")
			if resolveErr != nil {
				return Model{}, resolveErr
			}
			check.To = &resolved
		}
		check.Severity, err = canonicalTextPointer(value.Severity)
		if err != nil {
			return Model{}, err
		}
		if check.Severity == nil {
			severity := "error"
			check.Severity = &severity
		}
		// Unique-check columns form a set. Entity/relationship field tuples
		// elsewhere remain ordered because their positions carry meaning.
		projectedFields, err := projectUniqueCheckFields(value.Fields)
		if err != nil {
			return Model{}, err
		}
		if len(projectedFields) > 0 {
			check.Fields = &projectedFields
		}
		projectedValues, err := canonicalSet(value.Values)
		if err != nil {
			return Model{}, err
		}
		if len(projectedValues) > 0 {
			check.Values = &projectedValues
		}
		checks[index] = check
	}
	sort.SliceStable(checks, func(i, j int) bool { return projectedModelCheckKey(checks[i]) < projectedModelCheckKey(checks[j]) })
	for index := 1; index < len(checks); index++ {
		if checks[index-1].ID == checks[index].ID {
			return Model{}, fmt.Errorf("project Model: duplicate canonical check ID %q", checks[index].ID)
		}
	}
	grain, err := canonicalText(input.Spec.Grain.Entity)
	if err != nil {
		return Model{}, err
	}
	body := ModelContract{Definition: definition, Entities: entities, Grain: ModelGrain{Entity: grain}, Fields: fields}
	if len(checks) > 0 {
		body.Checks = &checks
	}
	return Model{payload: projectcontracts.ModelContractProjection{Profile: Profile, APIVersion: input.APIVersion, Kind: input.Kind, Metadata: metadata, Contract: body}}, nil
}

func projectMetadata(apiVersion, kind, wantKind string, authored authoredMetadata, contract Contract) (Metadata, error) {
	if apiVersion != "leapview.dev/v1" || kind != wantKind {
		return Metadata{}, fmt.Errorf("project %s: envelope is %q %q", wantKind, apiVersion, kind)
	}
	selected := contract
	if authored.Contract != nil {
		authoredContract := Contract{Version: authored.Contract.Version, Compatibility: authored.Contract.Compatibility}
		if (contract.Version != "" || contract.Compatibility != "") && authoredContract != contract {
			return Metadata{}, fmt.Errorf("project %s: authored contract metadata does not match constructor contract", wantKind)
		}
		selected = authoredContract
	}
	if selected.Version == "" || selected.Compatibility == "" {
		return Metadata{}, fmt.Errorf("project %s: contract version and compatibility are required", wantKind)
	}
	if !validSemanticVersion(selected.Version) {
		return Metadata{}, fmt.Errorf("project %s: contract version %q is not semantic versioning 2.0.0", wantKind, selected.Version)
	}
	if selected.Compatibility != "backward" {
		return Metadata{}, fmt.Errorf("project %s: unsupported compatibility %q", wantKind, selected.Compatibility)
	}
	id, err := canonicalText(authored.ID)
	if err != nil {
		return Metadata{}, err
	}
	name, err := canonicalText(authored.Name)
	if err != nil {
		return Metadata{}, err
	}
	version, err := canonicalText(selected.Version)
	if err != nil {
		return Metadata{}, err
	}
	compatibility, err := canonicalText(selected.Compatibility)
	if err != nil {
		return Metadata{}, err
	}
	return Metadata{ID: id, Name: name, Contract: Contract{Version: version, Compatibility: compatibility}}, nil
}

func projectField(value authoredField) (Field, error) {
	result := Field{Nullable: value.Nullable, CriticalDataElement: value.CriticalDataElement}
	var err error
	result.Datatype, err = canonicalTextPointer(value.Datatype)
	if err != nil {
		return Field{}, err
	}
	result.Classification, err = canonicalTextPointer(value.Classification)
	if err != nil {
		return Field{}, err
	}
	result.AuthoritativeDefinitions, err = projectAuthoritativeDefinitions(value.AuthoritativeDefinitions)
	if err != nil {
		return Field{}, err
	}
	result.Deprecation, err = projectFieldDeprecation(value.Deprecation)
	if err != nil {
		return Field{}, err
	}
	return result, nil
}

func projectModelField(value authoredField) (ModelField, error) {
	result := ModelField{Nullable: value.Nullable, CriticalDataElement: value.CriticalDataElement}
	var err error
	result.Datatype, err = canonicalTextPointer(value.Datatype)
	if err != nil {
		return ModelField{}, err
	}
	result.Classification, err = canonicalTextPointer(value.Classification)
	if err != nil {
		return ModelField{}, err
	}
	result.AuthoritativeDefinitions, err = projectAuthoritativeDefinitions(value.AuthoritativeDefinitions)
	if err != nil {
		return ModelField{}, err
	}
	result.Deprecation, err = projectFieldDeprecation(value.Deprecation)
	if err != nil {
		return ModelField{}, err
	}
	return result, nil
}

// Authoritative definitions are a set of typed links: no definition has
// precedence over another. Sorting and deduplicating after typed string
// normalization keeps source ordering from changing the contract identity.
func projectAuthoritativeDefinitions(values *[]authoredDefinition) (*[]projectcontracts.ContractProjectionAuthoritativeDefinition, error) {
	if values == nil {
		return nil, nil
	}
	result := make([]projectcontracts.ContractProjectionAuthoritativeDefinition, 0, len(*values))
	seen := make(map[string]struct{}, len(*values))
	for _, value := range *values {
		typeName, err := canonicalText(value.Type)
		if err != nil {
			return nil, err
		}
		url, err := canonicalURL(value.URL)
		if err != nil {
			return nil, err
		}
		item := projectcontracts.ContractProjectionAuthoritativeDefinition{Type: typeName, URL: url}
		key := typeName + "\x00" + url
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Type+"\x00"+result[i].URL < result[j].Type+"\x00"+result[j].URL
	})
	return &result, nil
}

func projectFieldDeprecation(value *authoredDeprecation) (*projectcontracts.ContractProjectionFieldDeprecation, error) {
	if value == nil {
		return nil, nil
	}
	since, err := canonicalText(value.Since)
	if err != nil {
		return nil, err
	}
	if !validSemanticVersion(since) {
		return nil, fmt.Errorf("deprecation since %q is not semantic versioning 2.0.0", since)
	}
	reason, err := canonicalText(value.Reason)
	if err != nil {
		return nil, err
	}
	replacement, err := canonicalTextPointer(value.Replacement)
	if err != nil {
		return nil, err
	}
	return &projectcontracts.ContractProjectionFieldDeprecation{Since: since, Reason: reason, Replacement: replacement}, nil
}

func projectDuration(value *authoredDuration) *Duration {
	if value == nil {
		return nil
	}
	return &Duration{Amount: value.Amount, Unit: value.Unit}
}

func decodeGenerated(input, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return decodeJSON(data, output)
}

func decodeJSON(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(output)
}

// validateAuthoredResource keeps the projection constructors fail-closed even
// when callers hand-build generated DTOs instead of obtaining them from the
// schema decoder. The generated schema is the authority for closed unions,
// required fields, identifiers, enum values, exact-number boundaries, and
// recursive semantic filter shape.
func validateAuthoredResource(kind configschema.Kind, value any) error {
	if err := validateInputEncoding(value); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal authored DTO: %w", err)
	}
	return configschema.ValidateBytes(kind, "<contractprojection>", encoded)
}

func projectedModelCheckKey(check ModelCheck) string {
	encoded, err := json.Marshal(check)
	if err != nil {
		return fmt.Sprintf("%T", check)
	}
	return string(encoded)
}

func projectUniqueCheckFields(fields []string) ([]string, error) {
	result, err := canonicalTexts(fields)
	if err != nil {
		return nil, err
	}
	sort.Strings(result)
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, fmt.Errorf("unique check repeats field %q", result[index])
		}
	}
	return result, nil
}

func referenceContextArgument(contexts []ReferenceContext) (*ReferenceContext, error) {
	if len(contexts) > 1 {
		return nil, errors.New("contract projection accepts at most one reference context")
	}
	if len(contexts) == 0 {
		return nil, nil
	}
	return &contexts[0], nil
}

func resolveProjectionReference(context *ReferenceContext, reference string, expected projectgraph.Kind, label string) (string, error) {
	if context == nil {
		return "", fmt.Errorf("%s requires a validated project reference context", label)
	}
	resolved, err := context.ResolveReference(reference, expected)
	if err != nil {
		return "", fmt.Errorf("%s %q: %w", label, reference, err)
	}
	return resolved.String(), nil
}

func resolveModelMemberReference(context *ReferenceContext, reference, label string) (string, error) {
	parts := strings.Split(strings.TrimSpace(reference), ".")
	if len(parts) < 2 || parts[len(parts)-1] == "" {
		return "", fmt.Errorf("%s %q: relationship target must name a model and field", label, reference)
	}
	modelReference := strings.Join(parts[:len(parts)-1], ".")
	resolved, err := resolveProjectionReference(context, modelReference, projectgraph.KindModel, label)
	if err != nil {
		return "", err
	}
	return resolved + "." + parts[len(parts)-1], nil
}
