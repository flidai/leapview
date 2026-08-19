// Command duckdbsqlgen generates the pinned descriptive DuckDB metadata
// artifact. It only uses the checked-in DuckDB runtime and never downloads
// source or extensions.
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/pkg/duckdbsql"
)

const (
	defaultOutput = "pkg/duckdbsql/metadata_generated.go"
	// SELECT * is intentional: assertColumns compares the complete runtime
	// schema with the pinned lock before any row is decoded. Adding, removing,
	// or reordering an upstream field therefore fails generation visibly.
	functionsSQL = `SELECT * FROM duckdb_functions()`
	keywordsSQL  = `SELECT * FROM duckdb_keywords()`
	typesSQL     = `SELECT * FROM duckdb_types()`
)

type schemaDescriptor struct {
	Discriminator  string
	AllowedFields  []string
	RequiredFields []string
}

type schemaFamilies struct {
	Statements  map[string]schemaDescriptor
	Relations   map[string]schemaDescriptor
	Expressions map[string]schemaDescriptor
	Modifiers   map[string]schemaDescriptor
	Supporting  map[string]schemaDescriptor
	Enums       enumSchema
}

type descriptorClass struct {
	Class   string `json:"class"`
	Base    string `json:"base"`
	Enum    string `json:"enum"`
	Members []struct {
		Name string `json:"name"`
	} `json:"members"`
}

type snapshotSchema struct {
	Discriminator  string   `json:"discriminator"`
	AllowedFields  []string `json:"allowed_fields"`
	RequiredFields []string `json:"required_fields"`
}

//go:embed ast_schema_snapshot.json
var astSchemaSnapshot []byte

func main() {
	var output, schemaOutput, source string
	var check bool
	flag.StringVar(&output, "output", defaultOutput, "generated Go output path")
	flag.StringVar(&schemaOutput, "schema-output", "pkg/duckdbsql/ast_schema_generated.go", "generated AST schema output path")
	flag.StringVar(&source, "source", os.Getenv("DUCKDB_SOURCE"), "pinned DuckDB source checkout (optional; validates descriptor provenance)")
	flag.BoolVar(&check, "check", false, "verify generated output is current")
	flag.Parse()

	if source != "" {
		if err := duckdbsql.ValidateSourceCheckout(source); err != nil {
			fatal(err)
		}
	}
	schemas, err := loadSchemas(source)
	if err != nil {
		fatal(err)
	}
	inventory, err := collectInventory()
	if err != nil {
		fatal(err)
	}
	content, err := render(inventory)
	if err != nil {
		fatal(err)
	}
	schemaContent, err := renderSchema(schemas)
	if err != nil {
		fatal(err)
	}
	if check {
		got, err := os.ReadFile(output)
		if err != nil {
			fatal(fmt.Errorf("read generated output: %w", err))
		}
		if string(got) != content {
			fatal(fmt.Errorf("%s is stale; run task duckdbsql:generate", output))
		}
		gotSchema, err := os.ReadFile(schemaOutput)
		if err != nil {
			fatal(fmt.Errorf("read generated schema output: %w", err))
		}
		if string(gotSchema) != schemaContent {
			fatal(fmt.Errorf("%s is stale; run task duckdbsql:generate", schemaOutput))
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(output, []byte(content), 0o644); err != nil {
		fatal(fmt.Errorf("write generated output: %w", err))
	}
	if err := os.MkdirAll(filepath.Dir(schemaOutput), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(schemaOutput, []byte(schemaContent), 0o644); err != nil {
		fatal(fmt.Errorf("write generated schema output: %w", err))
	}
}

func loadSchemas(source string) (schemaFamilies, error) {
	enums, err := loadEnumSchema(source)
	if err != nil {
		return schemaFamilies{}, err
	}
	if source == "" {
		schemas, err := loadSchemaSnapshot()
		if err != nil {
			return schemaFamilies{}, err
		}
		schemas.Enums = enums
		return schemas, nil
	}
	snapshot, err := loadSchemaSnapshot()
	if err != nil {
		return schemaFamilies{}, err
	}
	snapshot.Enums, err = loadEnumSnapshot()
	if err != nil {
		return schemaFamilies{}, err
	}
	identity, err := duckdbsql.UpstreamSourceIdentity()
	if err != nil {
		return schemaFamilies{}, err
	}
	families := schemaFamilies{
		Statements: map[string]schemaDescriptor{}, Relations: map[string]schemaDescriptor{},
		Expressions: map[string]schemaDescriptor{}, Modifiers: map[string]schemaDescriptor{}, Supporting: map[string]schemaDescriptor{},
		Enums: enums,
	}
	for family, filename := range map[string]string{
		"statement": "query_node.json", "relation": "tableref.json", "expression": "parsed_expression.json", "modifier": "result_modifier.json",
	} {
		classes, err := readDescriptorClasses(filepath.Join(source, identity.DescriptorRoot, filename))
		if err != nil {
			return schemaFamilies{}, fmt.Errorf("read %s descriptor: %w", filename, err)
		}
		for _, descriptor := range descriptorSchemas(classes) {
			switch family {
			case "statement":
				families.Statements[descriptor.Discriminator] = descriptor
			case "relation":
				families.Relations[descriptor.Discriminator] = descriptor
			case "expression":
				families.Expressions[descriptor.Discriminator] = descriptor
			case "modifier":
				families.Modifiers[descriptor.Discriminator] = descriptor
			}
		}
	}
	for _, filename := range []string{"nodes.json", "tableref.json", "statement.json"} {
		classes, err := readDescriptorClasses(filepath.Join(source, identity.DescriptorRoot, filename))
		if err != nil {
			return schemaFamilies{}, fmt.Errorf("read supporting %s descriptor: %w", filename, err)
		}
		for _, descriptor := range descriptorSchemas(classes) {
			if _, wanted := snapshot.Supporting[descriptor.Discriminator]; wanted {
				families.Supporting[descriptor.Discriminator] = descriptor
			}
		}
		for _, class := range classes {
			if class.Enum == "" {
				if _, wanted := snapshot.Supporting[class.Class]; wanted {
					families.Supporting[class.Class] = schemaDescriptor{Discriminator: class.Class, AllowedFields: classFields(classes, class.Class), RequiredFields: nil}
				}
			}
		}
	}
	if !reflect.DeepEqual(families, snapshot) {
		return schemaFamilies{}, errors.New("pinned DuckDB descriptors do not match ast_schema_snapshot.json")
	}
	return families, nil
}

func loadSchemaSnapshot() (schemaFamilies, error) {
	var raw map[string]map[string]snapshotSchema
	if err := json.Unmarshal(astSchemaSnapshot, &raw); err != nil {
		return schemaFamilies{}, fmt.Errorf("decode AST schema snapshot: %w", err)
	}
	convert := func(input map[string]snapshotSchema) map[string]schemaDescriptor {
		out := make(map[string]schemaDescriptor, len(input))
		for key, value := range input {
			out[key] = schemaDescriptor{Discriminator: value.Discriminator, AllowedFields: append([]string(nil), value.AllowedFields...), RequiredFields: append([]string(nil), value.RequiredFields...)}
		}
		return out
	}
	return schemaFamilies{Statements: convert(raw["statement"]), Relations: convert(raw["relation"]), Expressions: convert(raw["expression"]), Modifiers: convert(raw["modifier"]), Supporting: convert(raw["supporting"])}, nil
}

func readDescriptorClasses(path string) ([]descriptorClass, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var classes []descriptorClass
	if err := json.Unmarshal(data, &classes); err != nil {
		return nil, err
	}
	return classes, nil
}

func descriptorSchemas(classes []descriptorClass) []schemaDescriptor {
	byClass := make(map[string]descriptorClass, len(classes))
	for _, class := range classes {
		byClass[class.Class] = class
	}
	result := make([]schemaDescriptor, 0)
	for _, class := range classes {
		if class.Enum == "" {
			continue
		}
		fields := classFieldsFromMap(byClass, class.Class)
		result = append(result, schemaDescriptor{Discriminator: class.Enum, AllowedFields: fields, RequiredFields: []string{"type"}})
	}
	return result
}

func classFields(classes []descriptorClass, name string) []string {
	by := make(map[string]descriptorClass, len(classes))
	for _, class := range classes {
		by[class.Class] = class
	}
	return classFieldsFromMap(by, name)
}
func classFieldsFromMap(by map[string]descriptorClass, name string) []string {
	var fields []string
	seen := map[string]bool{}
	for name != "" {
		class, ok := by[name]
		if !ok {
			break
		}
		for _, member := range class.Members {
			if !seen[member.Name] {
				fields = append(fields, member.Name)
				seen[member.Name] = true
			}
		}
		name = class.Base
	}
	return fields
}

func fatal(err error) { fmt.Fprintln(os.Stderr, "duckdbsqlgen:", err); os.Exit(1) }

func collectInventory() (duckdbsql.MetadataInventory, error) {
	connector, err := duckdb.NewConnector(":memory:", func(execer driver.ExecerContext) error {
		ctx := context.Background()
		// Keep parser metadata generation hermetic. LOAD is explicit and occurs
		// before lock_configuration prevents subsequent configuration changes.
		for _, statement := range []string{
			"SET enable_external_access = false",
			"SET autoload_known_extensions = false",
			"SET autoinstall_known_extensions = false",
			"SET allow_persistent_secrets = false",
			"LOAD json",
			"SET lock_configuration = true",
		} {
			if _, err := execer.ExecContext(ctx, statement, nil); err != nil {
				return fmt.Errorf("DuckDB init %q: %w", statement, err)
			}
		}
		return nil
	})
	if err != nil {
		return duckdbsql.MetadataInventory{}, err
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx := context.Background()
	inventory := duckdbsql.MetadataInventory{}
	if err := readFunctions(ctx, db, &inventory); err != nil {
		return inventory, err
	}
	if err := readKeywords(ctx, db, &inventory); err != nil {
		return inventory, err
	}
	if err := readTypes(ctx, db, &inventory); err != nil {
		return inventory, err
	}
	duckdbsql.SortInventory(&inventory)
	return inventory, nil
}

func readFunctions(ctx context.Context, db *sql.DB, inventory *duckdbsql.MetadataInventory) error {
	rows, err := db.QueryContext(ctx, functionsSQL)
	if err != nil {
		return fmt.Errorf("query duckdb_functions(): %w", err)
	}
	defer rows.Close()
	if err := assertColumns(rows, "duckdb_functions"); err != nil {
		return err
	}
	for rows.Next() {
		var v [20]any
		dest := make([]any, len(v))
		for i := range v {
			dest[i] = &v[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		inventory.Functions = append(inventory.Functions, duckdbsql.FunctionMetadata{
			DatabaseName: str(v[0]), SchemaName: str(v[2]), FunctionName: str(v[3]), AliasOf: str(v[4]),
			FunctionType: str(v[5]), Description: str(v[6]), Comment: str(v[7]), Tags: stringMap(v[8]),
			ReturnType: str(v[9]), Parameters: strs(v[10]), ParameterTypes: strs(v[11]),
			Varargs: str(v[12]), MacroDefinition: str(v[13]), HasSideEffects: boolean(v[14]), Internal: boolean(v[15]),
			Stability: str(v[18]), Categories: strs(v[19]), Examples: strs(v[17]),
		})
	}
	return rows.Err()
}

func readKeywords(ctx context.Context, db *sql.DB, inventory *duckdbsql.MetadataInventory) error {
	rows, err := db.QueryContext(ctx, keywordsSQL)
	if err != nil {
		return fmt.Errorf("query duckdb_keywords(): %w", err)
	}
	defer rows.Close()
	if err := assertColumns(rows, "duckdb_keywords"); err != nil {
		return err
	}
	for rows.Next() {
		var name, category string
		if err := rows.Scan(&name, &category); err != nil {
			return err
		}
		inventory.Keywords = append(inventory.Keywords, duckdbsql.KeywordMetadata{Name: name, Category: category})
	}
	return rows.Err()
}

func readTypes(ctx context.Context, db *sql.DB, inventory *duckdbsql.MetadataInventory) error {
	rows, err := db.QueryContext(ctx, typesSQL)
	if err != nil {
		return fmt.Errorf("query duckdb_types(): %w", err)
	}
	defer rows.Close()
	if err := assertColumns(rows, "duckdb_types"); err != nil {
		return err
	}
	for rows.Next() {
		var v [13]any
		dest := make([]any, len(v))
		for i := range v {
			dest[i] = &v[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		inventory.Types = append(inventory.Types, duckdbsql.TypeMetadata{
			DatabaseName: str(v[0]), SchemaName: str(v[2]), TypeName: str(v[5]), TypeSize: int64Value(v[6]),
			LogicalType: str(v[7]), TypeCategory: str(v[8]), Comment: str(v[9]), Tags: stringMap(v[10]),
			Internal: boolean(v[11]), Labels: strs(v[12]),
		})
	}
	return rows.Err()
}

func assertColumns(rows *sql.Rows, table string) error {
	want, err := duckdbsql.InventoryColumns(table)
	if err != nil {
		return err
	}
	got, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("inspect %s schema: %w", table, err)
	}
	if len(got) != len(want) {
		return fmt.Errorf("%s schema has %d columns, want %d", table, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("%s column %d = %q, want %q", table, i, got[i], want[i])
		}
	}
	return nil
}

func str(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}

func boolean(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	default:
		return false
	}
}

func int64Value(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int8:
		return int64(v)
	case int16:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case uint:
		return int64(v)
	case uint8:
		return int64(v)
	case uint16:
		return int64(v)
	case uint32:
		return int64(v)
	case uint64:
		return int64(v)
	case float32:
		return int64(v)
	case float64:
		return int64(v)
	case []byte:
		parsed, _ := strconv.ParseInt(string(v), 10, 64)
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(v, 10, 64)
		return parsed
	default:
		return 0
	}
}

func stringMap(value any) map[string]string {
	if value == nil {
		return nil
	}
	switch v := value.(type) {
	case duckdb.OrderedMap:
		return orderedMapStrings(&v)
	case *duckdb.OrderedMap:
		if v == nil {
			return nil
		}
		return orderedMapStrings(v)
	case duckdb.Map:
		return mapAnyStrings(map[any]any(v))
	case map[any]any:
		return mapAnyStrings(v)
	case map[string]string:
		return cloneStringMap(v)
	case map[string]any:
		out := make(map[string]string, len(v))
		for key, item := range v {
			out[key] = str(item)
		}
		return out
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Map || rv.IsNil() {
		return nil
	}
	out := make(map[string]string, rv.Len())
	iter := rv.MapRange()
	for iter.Next() {
		out[str(iter.Key().Interface())] = str(iter.Value().Interface())
	}
	return out
}

func orderedMapStrings(value *duckdb.OrderedMap) map[string]string {
	keys, values := value.Keys(), value.Values()
	out := make(map[string]string, len(keys))
	for i, key := range keys {
		out[str(key)] = str(values[i])
	}
	return out
}

func mapAnyStrings(value map[any]any) map[string]string {
	out := make(map[string]string, len(value))
	for key, item := range value {
		out[str(key)] = str(item)
	}
	return out
}

func cloneStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	out := make(map[string]string, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func strs(value any) []string {
	if value == nil {
		return nil
	}
	if v, ok := value.([]string); ok {
		return append([]string(nil), v...)
	}
	if v, ok := value.([]any); ok {
		out := make([]string, 0, len(v))
		for _, item := range v {
			if item != nil {
				out = append(out, str(item))
			}
		}
		return out
	}
	// Keep this defensive for driver revisions that expose slices with a typed
	// element (e.g. []interface{} aliases).
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice {
		return nil
	}
	out := make([]string, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out = append(out, str(rv.Index(i).Interface()))
	}
	return out
}

func render(inventory duckdbsql.MetadataInventory) (string, error) {
	identity, err := duckdbsql.UpstreamSourceIdentity()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("// Code generated by internal/app/tools/duckdbsqlgen; DO NOT EDIT.\n")
	fmt.Fprintf(&b, "// DuckDB %s; upstream git commit %s; Go module %s %s; bindings module %s %s.\n", identity.DuckDBVersion, identity.DuckDBGitCommit, identity.GoModule, identity.GoModuleVersion, identity.BindingsModule, identity.BindingsVersion)
	b.WriteString("package duckdbsql\n\nconst (\n")
	fmt.Fprintf(&b, "\tDuckDBVersion = %q\n", identity.DuckDBVersion)
	fmt.Fprintf(&b, "\tDuckDBSourceCommit = %q\n", identity.DuckDBGitCommit)
	fmt.Fprintf(&b, "\tDuckDBGoModuleVersion = %q\n", identity.GoModuleVersion)
	fmt.Fprintf(&b, "\tDuckDBBindingsVersion = %q\n", identity.BindingsVersion)
	b.WriteString(")\n\n")
	b.WriteString("var generatedDescriptorManifest = []DescriptorProvenance{\n")
	for _, name := range sortedKeys(identity.DescriptorFileSHA) {
		fmt.Fprintf(&b, "\t{Path: %q, SHA256: %q},\n", filepath.Join(identity.DescriptorRoot, name), identity.DescriptorFileSHA[name])
	}
	b.WriteString("}\n\nvar generatedInventory = MetadataInventory{\n\tFunctions: []FunctionMetadata{\n")
	for _, row := range inventory.Functions {
		fmt.Fprintf(&b, "\t\t{DatabaseName:%q, SchemaName:%q, FunctionName:%q, AliasOf:%q, FunctionType:%q, Description:%q, Comment:%q, Tags:%s, ReturnType:%q, Parameters:%s, ParameterTypes:%s, Varargs:%q, MacroDefinition:%q, HasSideEffects:%t, Internal:%t, Stability:%q, Categories:%s, Examples:%s},\n", row.DatabaseName, row.SchemaName, row.FunctionName, row.AliasOf, row.FunctionType, row.Description, row.Comment, quoteStringMap(row.Tags), row.ReturnType, quoteStrings(row.Parameters), quoteStrings(row.ParameterTypes), row.Varargs, row.MacroDefinition, row.HasSideEffects, row.Internal, row.Stability, quoteStrings(row.Categories), quoteStrings(row.Examples))
	}
	b.WriteString("\t},\n\tKeywords: []KeywordMetadata{\n")
	for _, row := range inventory.Keywords {
		fmt.Fprintf(&b, "\t\t{Name:%q, Category:%q},\n", row.Name, row.Category)
	}
	b.WriteString("\t},\n\tTypes: []TypeMetadata{\n")
	for _, row := range inventory.Types {
		fmt.Fprintf(&b, "\t\t{DatabaseName:%q, SchemaName:%q, TypeName:%q, TypeSize:%d, LogicalType:%q, TypeCategory:%q, Comment:%q, Tags:%s, Internal:%t, Labels:%s},\n", row.DatabaseName, row.SchemaName, row.TypeName, row.TypeSize, row.LogicalType, row.TypeCategory, row.Comment, quoteStringMap(row.Tags), row.Internal, quoteStrings(row.Labels))
	}
	b.WriteString("\t},\n}\n")
	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return "", fmt.Errorf("format generated Go: %w", err)
	}
	return string(formatted), nil
}

func renderSchema(schemas schemaFamilies) (string, error) {
	identity, err := duckdbsql.UpstreamSourceIdentity()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("// Code generated by internal/app/tools/duckdbsqlgen from the pinned DuckDB serializer descriptors; DO NOT EDIT.\n")
	fmt.Fprintf(&b, "// DuckDB %s; upstream git commit %s.\n", identity.DuckDBVersion, identity.DuckDBGitCommit)
	for _, name := range sortedKeys(identity.DescriptorFileSHA) {
		fmt.Fprintf(&b, "// Descriptor %s SHA256 %s.\n", name, identity.DescriptorFileSHA[name])
	}
	b.WriteString("package duckdbsql\n\n")
	renderSchemaMaps(&b, schemas)
	renderEnumSchema(&b, schemas.Enums, identity.EnumFileSHA)
	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return "", fmt.Errorf("format generated schema Go: %w", err)
	}
	return string(formatted), nil
}

func renderSchemaMaps(b *strings.Builder, schemas schemaFamilies) {
	renderSchemaMap(b, "generatedStatementSchemas", schemas.Statements)
	renderSchemaMap(b, "generatedRelationSchemas", schemas.Relations)
	renderSchemaMap(b, "generatedExpressionSchemas", schemas.Expressions)
	renderSchemaMap(b, "generatedModifierSchemas", schemas.Modifiers)
	renderSchemaMap(b, "generatedSupportingSchemas", schemas.Supporting)
	b.WriteString("var generatedStatementVariants = []string{")
	renderKeys(b, schemas.Statements)
	b.WriteString("}\nvar generatedRelationVariants = []string{")
	renderKeys(b, schemas.Relations)
	b.WriteString("}\nvar generatedExpressionVariants = []string{")
	renderKeys(b, schemas.Expressions)
	b.WriteString("}\nvar generatedModifierVariants = []string{")
	renderKeys(b, schemas.Modifiers)
	b.WriteString("}\n")
}

func renderSchemaMap(b *strings.Builder, name string, schemas map[string]schemaDescriptor) {
	fmt.Fprintf(b, "var %s = map[string]serializedNodeSchema{\n", name)
	for _, key := range sortedSchemaKeys(schemas) {
		value := schemas[key]
		fmt.Fprintf(b, "\t%q: {Discriminator: %q, AllowedFields: %s, RequiredFields: %s},\n", key, value.Discriminator, quoteStrings(value.AllowedFields), quoteStrings(value.RequiredFields))
	}
	b.WriteString("}\n\n")
}

func renderKeys(b *strings.Builder, schemas map[string]schemaDescriptor) {
	for _, key := range sortedSchemaKeys(schemas) {
		fmt.Fprintf(b, "%q,", key)
	}
}

func sortedSchemaKeys(schemas map[string]schemaDescriptor) []string {
	keys := make([]string, 0, len(schemas))
	for key := range schemas {
		keys = append(keys, key)
	}
	sortStrings(keys)
	return keys
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sortStrings(keys)
	return keys
}
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
func quoteStrings(values []string) string {
	if len(values) == 0 {
		return "nil"
	}
	var b strings.Builder
	b.WriteString("[]string{")
	for i, value := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q", value)
	}
	b.WriteByte('}')
	return b.String()
}

func quoteStringMap(values map[string]string) string {
	if len(values) == 0 {
		return "nil"
	}
	var b strings.Builder
	b.WriteString("map[string]string{")
	for i, key := range sortedKeys(values) {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:%q", key, values[key])
	}
	b.WriteByte('}')
	return b.String()
}
