package model

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectors"
)

func (m *Model) Validate() error {
	return m.validate(false)
}

func (m *Model) ValidateAuthored() error {
	return m.validate(true)
}

func (m *Model) validate(authored bool) error {
	if m.Name == "" {
		return fmt.Errorf("semantic model name is required")
	}
	if len(m.Sources) == 0 {
		return fmt.Errorf("semantic model %q has no sources", m.Name)
	}
	for name, connection := range m.Connections {
		var (
			resolved Connection
			err      error
		)
		if authored {
			resolved, err = connection.ValidateAuthored(name)
		} else {
			resolved, err = connection.Validate(name)
		}
		if err != nil {
			return err
		}
		m.Connections[name] = resolved
	}
	if m.DefaultConnection != "" {
		if err := validateSemanticIdentifier(m.DefaultConnection); err != nil {
			return fmt.Errorf("default_connection %q is invalid: %w", m.DefaultConnection, err)
		}
		if _, ok := m.Connections[m.DefaultConnection]; !ok {
			return fmt.Errorf("default_connection %q references unknown connection", m.DefaultConnection)
		}
	}
	for name, source := range m.Sources {
		resolved, err := m.resolveSource(source)
		if err != nil {
			return fmt.Errorf("source %q: %w", name, err)
		}
		if err := resolved.Validate(name, m.Connections); err != nil {
			return err
		}
		for field, sourceField := range resolved.Fields {
			if err := validateSemanticIdentifier(field); err != nil {
				return fmt.Errorf("source %q field %q is invalid: %w", name, field, err)
			}
			sourceField.Field = name + "." + field
			sourceField.Table = name
			sourceField.Name = field
			resolved.Fields[field] = sourceField
		}
		m.Sources[name] = resolved
	}
	if len(m.Tables) == 0 {
		return fmt.Errorf("semantic model %q has no model tables", m.Name)
	}
	for name, table := range m.Tables {
		if err := validateSemanticIdentifier(name); err != nil {
			return fmt.Errorf("model table %q has invalid name: %w", name, err)
		}
		if table.SQL != "" && table.Transform.SQL == "" {
			table.Transform.SQL = table.SQL
		}
		if table.Source == "" && table.Transform.SQL == "" {
			return fmt.Errorf("model table %q requires source or transform.sql", name)
		}
		if table.Source != "" {
			if _, ok := m.Sources[table.Source]; !ok {
				return fmt.Errorf("model table %q references unknown source %q", name, table.Source)
			}
		}
		if len(table.SourceReads) > 0 {
			return fmt.Errorf("model table %q source_reads is no longer supported; source reads are inferred from transform.sql", name)
		}
		dependencies, err := m.modelTableSourceDependencies(name, table)
		if err != nil {
			return err
		}
		table.SourceDependencies = dependencies
		modelDependencies, err := m.modelTableModelDependencies(name, table)
		if err != nil {
			return err
		}
		table.ModelDependencies = modelDependencies
		if table.GrainEntity == "" {
			return fmt.Errorf("model table %q requires grain.entity", name)
		}
		grain, ok := table.Entities[table.GrainEntity]
		if !ok {
			return fmt.Errorf("model table %q grain.entity %q is not declared", name, table.GrainEntity)
		}
		if grain.Type != "primary" && grain.Type != "unique" {
			return fmt.Errorf("model table %q grain.entity %q must be primary or unique", name, table.GrainEntity)
		}
		if len(grain.Fields) == 0 {
			return fmt.Errorf("model table %q grain.entity %q requires fields", name, table.GrainEntity)
		}
		for entityName, entity := range table.Entities {
			if err := validateSemanticIdentifier(entityName); err != nil {
				return fmt.Errorf("model table %q entity %q is invalid: %w", name, entityName, err)
			}
			if len(entity.Fields) == 0 {
				return fmt.Errorf("model table %q entity %q requires fields", name, entityName)
			}
			for _, field := range entity.Fields {
				if err := validateSemanticIdentifier(field); err != nil {
					return fmt.Errorf("model table %q entity %q field %q is invalid: %w", name, entityName, field, err)
				}
			}
		}
		for field, dimension := range table.Dimensions {
			if err := validateSemanticIdentifier(field); err != nil {
				return fmt.Errorf("model table %q field %q is invalid: %w", name, field, err)
			}
			if err := validateLogicalDataType("model table "+name+" field "+field, dimension.Datatype); err != nil {
				return err
			}
			dimension.Field = name + "." + field
			dimension.Table = name
			dimension.Name = field
			if dimension.Label == "" {
				dimension.Label = titleFromIdentifier(field)
			}
			table.Dimensions[field] = dimension
		}
		columns, err := m.resolveModelColumns(name, table)
		if err != nil {
			return err
		}
		table.Columns = columns
		m.Tables[name] = table
	}
	seenRelationships := map[string]struct{}{}
	seenRelationshipEndpoints := map[string]string{}
	for index, relationship := range m.Relationships {
		if relationship.ID == "" || (!relationshipHasEndpoint(relationship, true) || !relationshipHasEndpoint(relationship, false)) {
			return fmt.Errorf("relationship %d requires id and structured from/to endpoints", index)
		}
		if _, exists := seenRelationships[relationship.ID]; exists {
			return fmt.Errorf("duplicate relationship id %q", relationship.ID)
		}
		seenRelationships[relationship.ID] = struct{}{}
		fromDataset, fromFields, fromErr := relationshipEndpoint(relationship, true)
		toDataset, toFields, toErr := relationshipEndpoint(relationship, false)
		if fromErr != nil || toErr != nil {
			// validateSemanticGraph reports the endpoint-specific diagnostic after
			// the shape checks above. Keep this pass focused on duplicate IDs and
			// normalized endpoint tuples.
			continue
		}
		endpointKey := fromDataset + "\x00" + strings.Join(fromFields, "\x00") + "\x00" + toDataset + "\x00" + strings.Join(toFields, "\x00")
		if previous, exists := seenRelationshipEndpoints[endpointKey]; exists {
			return fmt.Errorf("duplicate relationship definition for endpoints %q -> %q (relationships %q and %q)", fromDataset+"."+strings.Join(fromFields, ","), toDataset+"."+strings.Join(toFields, ","), previous, relationship.ID)
		}
		seenRelationshipEndpoints[endpointKey] = relationship.ID
	}
	if err := m.validateSemanticGraph(); err != nil {
		return err
	}
	return nil
}

func (m *Model) modelTableSourceDependencies(tableName string, table Table) ([]string, error) {
	sql := strings.TrimSpace(table.Transform.SQL)
	if sql == "" {
		sql = strings.TrimSpace(table.SQL)
	}
	hasSQL := sql != ""
	if hasSQL {
		if table.Source != "" {
			return nil, fmt.Errorf("model table %q uses transform.sql and must declare sources instead of source", tableName)
		}
		if err := validateModelSQLQuery(tableName, sql); err != nil {
			return nil, err
		}
	} else if table.Source == "" {
		return nil, fmt.Errorf("model table %q requires source or transform.sql", tableName)
	}
	seen := map[string]struct{}{}
	add := func(source string) error {
		source = strings.TrimSpace(source)
		if source == "" {
			return nil
		}
		if _, ok := m.Sources[source]; !ok {
			return fmt.Errorf("model table %q references unknown source %q", tableName, source)
		}
		seen[source] = struct{}{}
		return nil
	}
	if err := add(table.Source); err != nil {
		return nil, err
	}
	for _, source := range table.Sources {
		if err := add(source); err != nil {
			return nil, err
		}
	}
	inferred, rawRefs, unqualifiedRefs := m.modelSQLSourceRefs(sql)
	if len(rawRefs) > 0 {
		return nil, fmt.Errorf("model table %q model SQL must reference sources through source.<name>; raw.<name> is internal", tableName)
	}
	if len(unqualifiedRefs) > 0 {
		return nil, fmt.Errorf("model table %q SQL must reference sources through source.<name>; found unqualified relation %q", tableName, unqualifiedRefs[0])
	}
	for _, source := range inferred {
		if _, ok := m.Sources[source]; !ok {
			return nil, fmt.Errorf("model table %q SQL references unknown source %q", tableName, source)
		}
	}
	result := make([]string, 0, len(seen))
	for source := range seen {
		result = append(result, source)
	}
	sort.Strings(result)
	if hasSQL && !sameStringSet(result, inferred) {
		if len(result) == 0 && len(inferred) > 0 {
			return nil, fmt.Errorf("model table %q uses transform.sql and requires sources", tableName)
		}
		return nil, fmt.Errorf("model table %q SQL source references %v do not match declared sources %v", tableName, inferred, result)
	}
	return result, nil
}

func (m *Model) resolveModelColumns(tableName string, table Table) (map[string]ModelColumn, error) {
	if len(table.Columns) > 0 {
		columns := make(map[string]ModelColumn, len(table.Columns))
		for name, column := range table.Columns {
			if err := validateSemanticIdentifier(name); err != nil {
				return nil, fmt.Errorf("model table %q column %q is invalid: %w", tableName, name, err)
			}
			if column.Datatype == "" {
				if dimension, ok := table.Dimensions[name]; ok {
					column.Datatype = dimension.Datatype
				}
			}
			if err := validateLogicalDataType("model table "+tableName+" column "+name, column.Datatype); err != nil {
				return nil, err
			}
			if column.SourceField == "" {
				column.SourceField = name
			}
			if table.Source != "" && table.Transform.SQL == "" {
				if err := validateSemanticIdentifier(column.SourceField); err != nil {
					return nil, fmt.Errorf("model table %q column %q source_field %q is invalid: %w", tableName, name, column.SourceField, err)
				}
			}
			column.Name = name
			column.Field = tableName + "." + name
			columns[name] = column
		}
		if err := validateRequiredModelColumns(tableName, table, columns); err != nil {
			return nil, err
		}
		return columns, nil
	}
	columns := map[string]ModelColumn{}
	add := func(name string) {
		if name == "" {
			return
		}
		column := ModelColumn{Name: name, Field: tableName + "." + name, SourceField: name}
		if dimension, ok := table.Dimensions[name]; ok {
			column.Datatype = dimension.Datatype
		}
		columns[name] = column
	}
	for _, entity := range table.Entities {
		for _, field := range entity.Fields {
			add(field)
		}
	}
	for field := range table.Dimensions {
		add(field)
	}
	if m != nil {
		for _, metric := range m.Metrics {
			if metric.Type != "aggregate" || metric.Dataset != tableName || metric.Input == nil {
				continue
			}
			for _, ref := range []string{metric.Input.Field} {
				refTable, refField, ok := strings.Cut(ref, ".")
				if ok && refTable == tableName {
					add(refField)
				}
			}
		}
	}
	return columns, validateRequiredModelColumns(tableName, table, columns)
}

func validateRequiredModelColumns(tableName string, table Table, columns map[string]ModelColumn) error {
	require := func(field, reason string) error {
		if field == "" {
			return nil
		}
		if _, ok := columns[field]; !ok {
			return fmt.Errorf("model table %q column contract missing %s %q", tableName, reason, field)
		}
		return nil
	}
	for entityName, entity := range table.Entities {
		for _, field := range entity.Fields {
			if err := require(field, "entity "+entityName); err != nil {
				return err
			}
		}
	}
	for field := range table.Dimensions {
		if err := require(field, "field"); err != nil {
			return err
		}
	}
	return nil
}

func (m *Model) modelTableModelDependencies(tableName string, table Table) ([]string, error) {
	sql := strings.TrimSpace(table.Transform.SQL)
	if sql == "" {
		sql = strings.TrimSpace(table.SQL)
	}
	if sql == "" {
		return nil, nil
	}
	seen := map[string]struct{}{}
	for _, ref := range scanSQLRelationRefs(sql) {
		if ref.Namespace != "model" {
			continue
		}
		if ref.Name == tableName {
			return nil, fmt.Errorf("model table %q cannot read itself", tableName)
		}
		if _, ok := m.Tables[ref.Name]; !ok {
			return nil, fmt.Errorf("model table %q SQL references unknown model table %q", tableName, ref.Name)
		}
		seen[ref.Name] = struct{}{}
	}
	return sortedStringSet(seen), nil
}

func (m *Model) modelSQLSourceRefs(sql string) ([]string, []string, []string) {
	if sql == "" {
		return nil, nil, nil
	}
	sourceSeen := map[string]struct{}{}
	rawSeen := map[string]struct{}{}
	unqualifiedSeen := map[string]struct{}{}
	for _, ref := range scanSQLRelationRefs(sql) {
		switch ref.Namespace {
		case "source":
			sourceSeen[ref.Name] = struct{}{}
		case "raw":
			rawSeen[ref.Name] = struct{}{}
		case "":
			unqualifiedSeen[ref.Name] = struct{}{}
		}
	}
	sourceRefs := sortedStringSet(sourceSeen)
	rawRefs := sortedStringSet(rawSeen)
	unqualifiedRefs := sortedStringSet(unqualifiedSeen)
	return sourceRefs, rawRefs, unqualifiedRefs
}

func (m *Model) SQLSourceRefs(sql string) ([]string, []string, []string) {
	return m.modelSQLSourceRefs(sql)
}

func validateModelSQLQuery(tableName string, sql string) error {
	keyword, _, ok := firstSQLKeyword(sql)
	if !ok || (keyword != "select" && keyword != "with") {
		return fmt.Errorf("model table %q transform.sql must be a read-only SELECT or WITH query", tableName)
	}
	if keyword == "with" {
		start := scanSQLCTEs(sql, map[string]struct{}{}, &[]sqlRelationRef{})
		nextKeyword, _, ok := firstSQLKeyword(sql[start:])
		if !ok || nextKeyword != "select" {
			return fmt.Errorf("model table %q transform.sql must be a read-only SELECT or WITH query", tableName)
		}
	}
	return nil
}

func firstSQLKeyword(sql string) (string, int, bool) {
	for index := 0; index < len(sql); {
		switch sql[index] {
		case '\'':
			index = skipSQLSingleQuoted(sql, index)
			continue
		case '-':
			if index+1 < len(sql) && sql[index+1] == '-' {
				index = skipSQLLineComment(sql, index+2)
				continue
			}
		case '/':
			if index+1 < len(sql) && sql[index+1] == '*' {
				index = skipSQLBlockComment(sql, index+2)
				continue
			}
		}
		if isSQLIdentifierStart(sql[index]) {
			keyword, next, _ := readSQLIdentifier(sql, index)
			return strings.ToLower(keyword), next, true
		}
		index++
	}
	return "", len(sql), false
}

type sqlRelationRef struct {
	Namespace string
	Name      string
}

func scanSQLRelationRefs(sql string) []sqlRelationRef {
	return scanSQLRelationRefsWithLocals(sql, nil)
}

func scanSQLRelationRefsWithLocals(sql string, locals map[string]struct{}) []sqlRelationRef {
	refs := []sqlRelationRef{}
	localRefs := map[string]struct{}{}
	for name := range locals {
		localRefs[strings.ToLower(name)] = struct{}{}
	}
	start := scanSQLCTEs(sql, localRefs, &refs)
	for index := start; index < len(sql); {
		switch sql[index] {
		case '\'':
			index = skipSQLSingleQuoted(sql, index)
			continue
		case '-':
			if index+1 < len(sql) && sql[index+1] == '-' {
				index = skipSQLLineComment(sql, index+2)
				continue
			}
		case '/':
			if index+1 < len(sql) && sql[index+1] == '*' {
				index = skipSQLBlockComment(sql, index+2)
				continue
			}
		}
		if isSQLIdentifierStart(sql[index]) {
			keyword, next, _ := readSQLIdentifier(sql, index)
			if relationKeyword(strings.ToLower(keyword)) {
				relationRefs, relationNext := readSQLRelationList(sql, next, localRefs)
				refs = append(refs, relationRefs...)
				index = relationNext
				continue
			}
			index = next
			continue
		}
		index++
	}
	return refs
}

func scanSQLCTEs(sql string, locals map[string]struct{}, refs *[]sqlRelationRef) int {
	keyword, next, ok := firstSQLKeyword(sql)
	if !ok || keyword != "with" {
		return 0
	}
	index := skipSQLSpaces(sql, next)
	if recursive, afterRecursive, ok := readSQLIdentifier(sql, index); ok && strings.EqualFold(recursive, "recursive") {
		index = skipSQLSpaces(sql, afterRecursive)
	}
	for {
		name, afterName, ok := readSQLIdentifier(sql, index)
		if !ok {
			return index
		}
		locals[strings.ToLower(name)] = struct{}{}
		index = skipSQLSpaces(sql, afterName)
		if index < len(sql) && sql[index] == '(' {
			index = skipSQLBalanced(sql, index)
			index = skipSQLSpaces(sql, index)
		}
		asKeyword, afterAS, ok := readSQLIdentifier(sql, index)
		if !ok || !strings.EqualFold(asKeyword, "as") {
			return index
		}
		index = skipSQLSpaces(sql, afterAS)
		if index >= len(sql) || sql[index] != '(' {
			return index
		}
		inside, afterBody := readSQLBalancedContent(sql, index)
		*refs = append(*refs, scanSQLRelationRefsWithLocals(inside, locals)...)
		index = skipSQLSpaces(sql, afterBody)
		if index >= len(sql) || sql[index] != ',' {
			return index
		}
		index = skipSQLSpaces(sql, index+1)
	}
}

func relationKeyword(keyword string) bool {
	switch keyword {
	case "from", "join":
		return true
	default:
		return false
	}
}

func readSQLRelationList(sql string, index int, locals map[string]struct{}) ([]sqlRelationRef, int) {
	refs := []sqlRelationRef{}
	for {
		index = skipSQLSpaces(sql, index)
		if index >= len(sql) {
			return refs, index
		}
		if sql[index] == '(' {
			inside, next := readSQLBalancedContent(sql, index)
			refs = append(refs, scanSQLRelationRefsWithLocals(inside, locals)...)
			index = next
			return refs, index
		}
		ref, next, ok := readSQLRelationRef(sql, index, locals)
		if !ok {
			return refs, index
		}
		refs = append(refs, ref)
		index = skipSQLRelationAlias(sql, next)
		index = skipSQLSpaces(sql, index)
		if index >= len(sql) || sql[index] != ',' {
			return refs, index
		}
		index++
	}
}

func readSQLRelationRef(sql string, index int, locals map[string]struct{}) (sqlRelationRef, int, bool) {
	first, next, ok := readSQLIdentifier(sql, index)
	if !ok {
		return sqlRelationRef{}, index, false
	}
	dot := skipSQLSpaces(sql, next)
	if dot < len(sql) && sql[dot] == '.' {
		nameStart := skipSQLSpaces(sql, dot+1)
		name, afterName, ok := readSQLIdentifier(sql, nameStart)
		if !ok {
			return sqlRelationRef{}, index, false
		}
		namespace := strings.ToLower(first)
		if namespace == "source" || namespace == "raw" || namespace == "model" {
			return sqlRelationRef{Namespace: namespace, Name: name}, afterName, true
		}
		return sqlRelationRef{Name: name}, afterName, true
	}
	if _, ok := locals[strings.ToLower(first)]; ok {
		return sqlRelationRef{Namespace: "local", Name: first}, next, true
	}
	return sqlRelationRef{Name: first}, next, true
}

func readSQLIdentifier(sql string, index int) (string, int, bool) {
	if index >= len(sql) {
		return "", index, false
	}
	if sql[index] == '"' {
		var builder strings.Builder
		for cursor := index + 1; cursor < len(sql); cursor++ {
			if sql[cursor] == '"' {
				if cursor+1 < len(sql) && sql[cursor+1] == '"' {
					builder.WriteByte('"')
					cursor++
					continue
				}
				return builder.String(), cursor + 1, true
			}
			builder.WriteByte(sql[cursor])
		}
		return "", len(sql), false
	}
	if !isSQLIdentifierStart(sql[index]) {
		return "", index, false
	}
	cursor := index + 1
	for cursor < len(sql) && isSQLIdentifierPart(sql[cursor]) {
		cursor++
	}
	return sql[index:cursor], cursor, true
}

func skipSQLSingleQuoted(sql string, index int) int {
	for cursor := index + 1; cursor < len(sql); cursor++ {
		if sql[cursor] == '\'' {
			if cursor+1 < len(sql) && sql[cursor+1] == '\'' {
				cursor++
				continue
			}
			return cursor + 1
		}
	}
	return len(sql)
}

func skipSQLLineComment(sql string, index int) int {
	for index < len(sql) && sql[index] != '\n' && sql[index] != '\r' {
		index++
	}
	return index
}

func skipSQLBlockComment(sql string, index int) int {
	for index+1 < len(sql) {
		if sql[index] == '*' && sql[index+1] == '/' {
			return index + 2
		}
		index++
	}
	return len(sql)
}

func skipSQLBalanced(sql string, index int) int {
	_, next := readSQLBalancedContent(sql, index)
	return next
}

func readSQLBalancedContent(sql string, index int) (string, int) {
	depth := 0
	start := index + 1
	for index < len(sql) {
		switch sql[index] {
		case '\'':
			index = skipSQLSingleQuoted(sql, index)
			continue
		case '"':
			_, next, ok := readSQLIdentifier(sql, index)
			if ok {
				index = next
				continue
			}
		case '-':
			if index+1 < len(sql) && sql[index+1] == '-' {
				index = skipSQLLineComment(sql, index+2)
				continue
			}
		case '/':
			if index+1 < len(sql) && sql[index+1] == '*' {
				index = skipSQLBlockComment(sql, index+2)
				continue
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return sql[start:index], index + 1
			}
		}
		index++
	}
	return sql[start:], index
}

func skipSQLRelationAlias(sql string, index int) int {
	index = skipSQLSpaces(sql, index)
	if index >= len(sql) {
		return index
	}
	if sql[index] == '(' {
		return skipSQLBalanced(sql, index)
	}
	if sql[index] == '"' {
		_, next, ok := readSQLIdentifier(sql, index)
		if ok {
			return next
		}
		return index
	}
	if !isSQLIdentifierStart(sql[index]) {
		return index
	}
	value, next, _ := readSQLIdentifier(sql, index)
	lower := strings.ToLower(value)
	if lower == "as" {
		return skipSQLRelationAlias(sql, next)
	}
	if relationListTerminator(lower) {
		return index
	}
	return next
}

func relationListTerminator(value string) bool {
	switch value {
	case "set", "where", "group", "order", "having", "limit", "offset", "qualify", "union", "except", "intersect", "join", "left", "right", "full", "inner", "outer", "cross", "on", "using":
		return true
	default:
		return false
	}
}

func skipSQLSpaces(sql string, index int) int {
	for index < len(sql) {
		switch sql[index] {
		case ' ', '\n', '\r', '\t', '\f':
			index++
		default:
			return index
		}
	}
	return index
}

func isSQLIdentifierStart(char byte) bool {
	return char == '_' || (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z')
}

func isSQLIdentifierPart(char byte) bool {
	return isSQLIdentifierStart(char) || (char >= '0' && char <= '9')
}

func sortedStringSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sameStringSet(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (m *Model) validateSemanticGraph() error {
	if len(m.Datasets) == 0 {
		return fmt.Errorf("semantic model requires at least one dataset")
	}
	if err := m.validateExecutionDatasetsAndTables(); err != nil {
		return err
	}
	if err := validateRelationshipEndpointDuplicates(m.Relationships); err != nil {
		return err
	}
	relationships := append([]Relationship(nil), m.Relationships...)
	sort.SliceStable(relationships, func(i, j int) bool {
		if relationships[i].ID != relationships[j].ID {
			return relationships[i].ID < relationships[j].ID
		}
		return relationshipEndpointDisplay(relationships[i], true)+"\x00"+relationshipEndpointDisplay(relationships[i], false) < relationshipEndpointDisplay(relationships[j], true)+"\x00"+relationshipEndpointDisplay(relationships[j], false)
	})
	seenRelationshipIDs := map[string]struct{}{}
	for _, relationship := range relationships {
		if relationship.ID == "" {
			return fmt.Errorf("relationship requires id")
		}
		if err := validateSemanticIdentifier(relationship.ID); err != nil {
			return fmt.Errorf("relationship %q id is invalid: %w", relationship.ID, err)
		}
		if _, exists := seenRelationshipIDs[relationship.ID]; exists {
			return fmt.Errorf("duplicate relationship id %q", relationship.ID)
		}
		seenRelationshipIDs[relationship.ID] = struct{}{}
		if relationship.Cardinality != "many_to_one" && relationship.Cardinality != "one_to_one" {
			return fmt.Errorf(
				"relationship %q has unsafe cardinality %q from %q to %q",
				relationship.ID,
				relationship.Cardinality,
				relationshipEndpointDisplay(relationship, true),
				relationshipEndpointDisplay(relationship, false),
			)
		}
		fromTable, fromFields, err := m.validateRelationshipEndpoint("from", relationship, true)
		if err != nil {
			return err
		}
		toTable, toFields, err := m.validateRelationshipEndpoint("to", relationship, false)
		if err != nil {
			return err
		}
		if len(fromFields) != len(toFields) {
			return fmt.Errorf("relationship %q endpoint tuple arity mismatch: from %d fields, to %d fields", relationship.ID, len(fromFields), len(toFields))
		}
		if err := validateRelationshipTuple(relationship.ID, "from", fromFields); err != nil {
			return err
		}
		if err := validateRelationshipTuple(relationship.ID, "to", toFields); err != nil {
			return err
		}
		for index := range fromFields {
			left := m.Tables[fromTable].Dimensions[fromFields[index]]
			right := m.Tables[toTable].Dimensions[toFields[index]]
			if !relationshipTypesCompatible(left, right) {
				return fmt.Errorf("relationship %q endpoint field %q type %q is incompatible with %q type %q", relationship.ID, fromTable+"."+fromFields[index], relationshipFieldType(left), toTable+"."+toFields[index], relationshipFieldType(right))
			}
		}
		if relationship.Cardinality == "one_to_one" {
			if err := m.requireRelationshipPrimaryKey(relationship, fromTable, fromFields); err != nil {
				return err
			}
		}
		if err := m.requireRelationshipPrimaryKey(relationship, toTable, toFields); err != nil {
			return err
		}
	}
	if err := m.validateDirectionalRelationshipCycles(); err != nil {
		return err
	}
	return m.validateSemanticDefinitions()
}

// validateExecutionDatasetsAndTables validates the lowered serving graph. A
// semantic model's dataset aliases are the runtime table namespace; allowing
// an extra table or an unbound dataset would let direct construction bypass
// the authored project binding performed by the project compiler.
func (m *Model) validateExecutionDatasetsAndTables() error {
	datasetNames := make([]string, 0, len(m.Datasets))
	for name := range m.Datasets {
		datasetNames = append(datasetNames, name)
	}
	sort.Strings(datasetNames)
	for _, datasetName := range datasetNames {
		if err := validateSemanticIdentifier(datasetName); err != nil {
			return fmt.Errorf("semantic dataset %q is invalid: %w", datasetName, err)
		}
		dataset := m.Datasets[datasetName]
		if strings.TrimSpace(dataset.Model) == "" {
			return fmt.Errorf("semantic dataset %q model is required", datasetName)
		}
		if err := validateModelBindingName(dataset.Model); err != nil {
			return fmt.Errorf("semantic dataset %q model %q is invalid: %w", datasetName, dataset.Model, err)
		}
		if _, ok := m.Tables[datasetName]; !ok {
			return fmt.Errorf("semantic dataset %q has no runtime table", datasetName)
		}
		if table := m.Tables[datasetName]; table.ModelName != "" && table.ModelName != dataset.Model {
			return fmt.Errorf("semantic dataset %q model binding %q does not match runtime table model %q", datasetName, dataset.Model, table.ModelName)
		}
	}
	tableNames := make([]string, 0, len(m.Tables))
	for name := range m.Tables {
		tableNames = append(tableNames, name)
	}
	sort.Strings(tableNames)
	for _, tableName := range tableNames {
		if err := validateSemanticIdentifier(tableName); err != nil {
			return fmt.Errorf("model table %q has invalid name: %w", tableName, err)
		}
		if _, ok := m.Datasets[tableName]; !ok {
			return fmt.Errorf("model table %q is not bound to a semantic dataset", tableName)
		}
		if err := validateExecutionTable(tableName, m.Tables[tableName]); err != nil {
			return err
		}
	}
	return validateExecutionTimeSemantics(m)
}

func validateExecutionTimeSemantics(m *Model) error {
	names := make([]string, 0, len(m.Dimensions))
	for name := range m.Dimensions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		dimension := m.Dimensions[name]
		// Validate values before the closed-union shape so malformed grains
		// retain a precise, deterministic diagnostic.
		for _, grain := range dimension.Grains {
			if _, ok := supportedTimeGrains[grain]; !ok {
				return fmt.Errorf("semantic dimension %q has unsupported time grain %q", name, grain)
			}
		}
		seen := map[string]struct{}{}
		for _, grain := range dimension.Grains {
			if _, exists := seen[grain]; exists {
				return fmt.Errorf("semantic dimension %q declares duplicate time grain %q", name, grain)
			}
			seen[grain] = struct{}{}
		}
		if dimension.NativeGrain == "" && len(dimension.Grains) > 0 {
			return fmt.Errorf("semantic dimension %q time semantics requires native grain when grains are declared", name)
		}
		if dimension.NativeGrain != "" && len(dimension.Grains) == 0 {
			return fmt.Errorf("semantic dimension %q time semantics requires grains when native grain is declared", name)
		}
	}
	return nil
}

func validateExecutionTable(tableName string, table Table) error {
	if len(table.Entities) == 0 {
		return fmt.Errorf("model table %q requires at least one entity", tableName)
	}
	if strings.TrimSpace(table.GrainEntity) == "" {
		return fmt.Errorf("model table %q requires grain.entity", tableName)
	}
	grain, ok := table.Entities[table.GrainEntity]
	if !ok {
		return fmt.Errorf("model table %q grain.entity %q is not declared", tableName, table.GrainEntity)
	}
	if grain.Type != "primary" && grain.Type != "unique" {
		return fmt.Errorf("model table %q grain.entity %q must be primary or unique", tableName, table.GrainEntity)
	}
	if len(grain.Fields) == 0 {
		return fmt.Errorf("model table %q grain.entity %q requires fields", tableName, table.GrainEntity)
	}
	entityNames := make([]string, 0, len(table.Entities))
	for name := range table.Entities {
		entityNames = append(entityNames, name)
	}
	sort.Strings(entityNames)
	for _, entityName := range entityNames {
		entity := table.Entities[entityName]
		if err := validateSemanticIdentifier(entityName); err != nil {
			return fmt.Errorf("model table %q entity %q is invalid: %w", tableName, entityName, err)
		}
		switch entity.Type {
		case "primary", "unique", "foreign", "natural":
		default:
			return fmt.Errorf("model table %q entity %q has unsupported type %q", tableName, entityName, entity.Type)
		}
		if len(entity.Fields) == 0 {
			return fmt.Errorf("model table %q entity %q requires fields", tableName, entityName)
		}
		seenFields := map[string]struct{}{}
		for _, field := range entity.Fields {
			if err := validateSemanticIdentifier(field); err != nil {
				return fmt.Errorf("model table %q entity %q field %q is invalid: %w", tableName, entityName, field, err)
			}
			if _, exists := seenFields[field]; exists {
				return fmt.Errorf("model table %q entity %q contains duplicate field %q", tableName, entityName, field)
			}
			seenFields[field] = struct{}{}
			if _, ok := table.Dimensions[field]; !ok {
				return fmt.Errorf("model table %q entity %q field %q is not declared", tableName, entityName, field)
			}
		}
	}
	dimensionNames := make([]string, 0, len(table.Dimensions))
	for name := range table.Dimensions {
		dimensionNames = append(dimensionNames, name)
	}
	sort.Strings(dimensionNames)
	for _, field := range dimensionNames {
		if err := validateSemanticIdentifier(field); err != nil {
			return fmt.Errorf("model table %q field %q is invalid: %w", tableName, field, err)
		}
		if err := validateLogicalDataType("model table "+tableName+" field "+field, table.Dimensions[field].Datatype); err != nil {
			return err
		}
	}
	columnNames := make([]string, 0, len(table.Columns))
	for name := range table.Columns {
		columnNames = append(columnNames, name)
	}
	sort.Strings(columnNames)
	for _, field := range columnNames {
		column := table.Columns[field]
		if err := validateSemanticIdentifier(field); err != nil {
			return fmt.Errorf("model table %q column %q is invalid: %w", tableName, field, err)
		}
		if err := validateLogicalDataType("model table "+tableName+" column "+field, column.Datatype); err != nil {
			return err
		}
		if _, ok := table.Dimensions[field]; !ok {
			return fmt.Errorf("model table %q column %q is not declared as a field", tableName, field)
		}
	}
	// A lowered table may omit Columns when it was built directly in memory;
	// in that case Dimensions are the executable column contract. When Columns
	// are present, however, every semantic field and entity key must be backed
	// by one explicitly typed column.
	if len(table.Columns) > 0 {
		for _, field := range dimensionNames {
			if _, ok := table.Columns[field]; !ok {
				return fmt.Errorf("model table %q column contract missing field %q", tableName, field)
			}
		}
	}
	return nil
}

func validateRelationshipTuple(id, role string, fields []string) error {
	seen := map[string]struct{}{}
	for _, field := range fields {
		if err := validateSemanticIdentifier(field); err != nil {
			return fmt.Errorf("relationship %q %s endpoint field %q is invalid: %w", id, role, field, err)
		}
		if _, exists := seen[field]; exists {
			return fmt.Errorf("relationship %q %s endpoint contains duplicate field %q", id, role, field)
		}
		seen[field] = struct{}{}
	}
	return nil
}

func validateRelationshipEndpointDuplicates(relationships []Relationship) error {
	ordered := append([]Relationship(nil), relationships...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	seen := map[string]string{}
	for _, relationship := range ordered {
		fromDataset, fromFields, fromErr := relationshipEndpoint(relationship, true)
		toDataset, toFields, toErr := relationshipEndpoint(relationship, false)
		if fromErr != nil || toErr != nil {
			continue
		}
		key := fromDataset + "\x00" + strings.Join(fromFields, "\x00") + "\x00" + toDataset + "\x00" + strings.Join(toFields, "\x00")
		if previous, exists := seen[key]; exists {
			return fmt.Errorf("duplicate relationship definition for endpoints %q -> %q (relationships %q and %q)", fromDataset+"."+strings.Join(fromFields, ","), toDataset+"."+strings.Join(toFields, ","), previous, relationship.ID)
		}
		seen[key] = relationship.ID
	}
	return nil
}

// validateDirectionalRelationshipCycles rejects cycles in the authored
// relationship direction (from -> to). Safe traversal deliberately has a
// narrower rule for one-to-one reverse edges, but allowing a directional cycle
// would make relationship ownership and deterministic path resolution
// ambiguous. Relationships and adjacency lists are sorted so the diagnostic is
// stable across map/slice construction order.
func (m *Model) validateDirectionalRelationshipCycles() error {
	type edge struct {
		to string
		id string
	}
	adjacency := map[string][]edge{}
	vertices := map[string]struct{}{}
	for _, relationship := range m.Relationships {
		from, _, fromErr := relationshipEndpoint(relationship, true)
		to, _, toErr := relationshipEndpoint(relationship, false)
		if fromErr != nil || toErr != nil {
			continue
		}
		vertices[from] = struct{}{}
		vertices[to] = struct{}{}
		adjacency[from] = append(adjacency[from], edge{to: to, id: relationship.ID})
	}
	for from := range adjacency {
		sort.Slice(adjacency[from], func(i, j int) bool {
			if adjacency[from][i].to != adjacency[from][j].to {
				return adjacency[from][i].to < adjacency[from][j].to
			}
			return adjacency[from][i].id < adjacency[from][j].id
		})
	}
	orderedVertices := make([]string, 0, len(vertices))
	for vertex := range vertices {
		orderedVertices = append(orderedVertices, vertex)
	}
	sort.Strings(orderedVertices)
	state := map[string]uint8{}
	stack := []string{}
	stackIndex := map[string]int{}
	var visit func(string) error
	visit = func(vertex string) error {
		state[vertex] = 1
		stackIndex[vertex] = len(stack)
		stack = append(stack, vertex)
		for _, next := range adjacency[vertex] {
			switch state[next.to] {
			case 0:
				if err := visit(next.to); err != nil {
					return err
				}
			case 1:
				start := stackIndex[next.to]
				cycle := append([]string(nil), stack[start:]...)
				cycle = append(cycle, next.to)
				return fmt.Errorf("relationship cycle detected in directional graph: %s", strings.Join(cycle, " -> "))
			}
		}
		stack = stack[:len(stack)-1]
		delete(stackIndex, vertex)
		state[vertex] = 2
		return nil
	}
	for _, vertex := range orderedVertices {
		if state[vertex] == 0 {
			if err := visit(vertex); err != nil {
				return err
			}
		}
	}
	return nil
}

func relationshipFieldType(field MetricDimension) string {
	if field.Datatype != "" {
		return string(field.Datatype)
	}
	return field.Type
}

func relationshipTypesCompatible(left, right MetricDimension) bool {
	return left.Datatype != "" && right.Datatype != "" && left.Datatype == right.Datatype
}

func (m *Model) requireRelationshipPrimaryKey(relationship Relationship, tableName string, fields []string) error {
	table := m.Tables[tableName]
	if len(table.Entities) > 0 {
		for _, entity := range table.Entities {
			if entity.Type != "primary" && entity.Type != "unique" {
				continue
			}
			if sameStringSet(entity.Fields, fields) {
				return nil
			}
		}
		return fmt.Errorf(
			"relationship %q %s endpoint %q must belong to a primary or unique entity of table %q",
			relationship.ID,
			relationship.Cardinality,
			tableName+"."+strings.Join(fields, ","),
			tableName,
		)
	}
	return fmt.Errorf(
		"relationship %q %s endpoint %q must belong to a primary or unique entity of table %q",
		relationship.ID,
		relationship.Cardinality,
		tableName+"."+strings.Join(fields, ","),
		tableName,
	)
}

func (m *Model) validateRelationshipEndpoint(role string, relationship Relationship, from bool) (string, []string, error) {
	tableName, fields, err := relationshipEndpoint(relationship, from)
	if err != nil {
		return "", nil, fmt.Errorf("relationship %s %q: %w", role, relationshipEndpointDisplay(relationship, from), err)
	}
	if err := validateSemanticIdentifier(tableName); err != nil {
		return "", nil, fmt.Errorf("relationship %s endpoint dataset %q is invalid: %w", role, tableName, err)
	}
	if _, ok := m.Datasets[tableName]; !ok {
		return "", nil, fmt.Errorf("relationship %s %q references unknown semantic dataset %q", role, relationshipEndpointDisplay(relationship, from), tableName)
	}
	table, ok := m.Tables[tableName]
	if !ok {
		return "", nil, fmt.Errorf("relationship %s %q references unknown table %q", role, relationshipEndpointDisplay(relationship, from), tableName)
	}
	for _, fieldName := range fields {
		if _, ok := table.Dimensions[fieldName]; !ok {
			return "", nil, fmt.Errorf("relationship %s %q references unknown field %q on table %q", role, relationshipEndpointDisplay(relationship, from), fieldName, tableName)
		}
	}
	return tableName, fields, nil
}

func relationshipHasEndpoint(relationship Relationship, from bool) bool {
	_, fields, err := relationshipEndpoint(relationship, from)
	return err == nil && len(fields) > 0
}

func relationshipEndpoint(relationship Relationship, from bool) (string, []string, error) {
	dataset, fields := relationship.FromDataset, relationship.FromFields
	if !from {
		dataset, fields = relationship.ToDataset, relationship.ToFields
	}
	if dataset == "" || len(fields) == 0 {
		return "", nil, fmt.Errorf("endpoint requires dataset and non-empty fields")
	}
	return dataset, append([]string(nil), fields...), nil
}

func relationshipEndpointDisplay(relationship Relationship, from bool) string {
	dataset, fields, err := relationshipEndpoint(relationship, from)
	if err == nil {
		return dataset + "." + strings.Join(fields, ",")
	}
	if from {
		dataset, fields = relationship.FromDataset, relationship.FromFields
	} else {
		dataset, fields = relationship.ToDataset, relationship.ToFields
	}
	if dataset == "" && len(fields) == 0 {
		return "<missing>"
	}
	return dataset + "." + strings.Join(fields, ",")
}

// RelationshipEndpoint returns the ordered physical tuple for one side of a
// validated relationship. Canonical composite identities remain tuples all the
// way to the query planner; callers must not select a single field.
func RelationshipEndpoint(relationship Relationship, from bool) (string, []string, error) {
	return relationshipEndpoint(relationship, from)
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func titleFromIdentifier(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func (m *Model) resolveSource(source Source) (Source, error) {
	switch source.Kind() {
	case KindPath, KindObject:
		if source.Connection == "" {
			source.Connection = m.DefaultConnection
		}
		if source.Connection == "" {
			return source, fmt.Errorf("requires connection")
		}
		connection, ok := m.Connections[source.Connection]
		if !ok {
			return source, fmt.Errorf("references unknown connection %q", source.Connection)
		}
		if source.Path != "" {
			if len(connection.Defaults.Options) > 0 {
				options := make(map[string]any, len(connection.Defaults.Options)+len(source.Options))
				for key, value := range connection.Defaults.Options {
					options[key] = value
				}
				for key, value := range source.Options {
					options[key] = value
				}
				source.Options = options
			}
			if source.Format == "" {
				format, ok := InferFormat(source.Path)
				if !ok {
					return source, fmt.Errorf("path %q requires format", source.Path)
				}
				source.Format = format
			}
		}
		return source, nil
	default:
		return source, nil
	}
}

func (s Source) Validate(name string, connections map[string]Connection) error {
	if err := validateSemanticIdentifier(name); err != nil {
		return fmt.Errorf("source %q has invalid name: %w", name, err)
	}
	for key := range s.Options {
		if err := validateSemanticIdentifier(key); err != nil {
			return fmt.Errorf("source %q option %q is invalid: %w", name, key, err)
		}
	}
	switch s.Kind() {
	case KindPath:
		if s.Connection == "" {
			return fmt.Errorf("source %q requires connection", name)
		}
		connection, ok := connections[s.Connection]
		if !ok {
			return fmt.Errorf("source %q references unknown connection %q", name, s.Connection)
		}
		connectionSpec, ok := LookupConnection(connection.Kind)
		if !ok || !connectionSpec.AllowsPathSource {
			return fmt.Errorf("source %q path cannot use %s connection %q", name, connection.Kind, s.Connection)
		}
		if connection.Kind == "managed" && !IsLocalPath(s.Path) {
			return fmt.Errorf("source %q %s connection %q cannot use remote path %q", name, connection.Kind, s.Connection, s.Path)
		}
		if connection.Kind == "managed" {
			cleaned := filepath.Clean(s.Path)
			if filepath.IsAbs(s.Path) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
				return fmt.Errorf("source %q managed path %q must be relative and cannot contain traversal", name, s.Path)
			}
		}
		if !sourceWithinConnectionScope(connection, s.Path) {
			return fmt.Errorf("source %q path %q escapes connection scope", name, s.Path)
		}
		if connectionSpec.AllowsPathSource && connection.Kind != "managed" && IsLocalPath(s.Path) && connection.Scope == "" {
			return fmt.Errorf("source %q remote connection %q requires scope for relative path %q", name, s.Connection, s.Path)
		}
		if s.Format == "" {
			return fmt.Errorf("source %q path requires format", name)
		}
		formatSpec, ok := LookupFormat(s.Format)
		if !ok {
			return fmt.Errorf("source %q has unsupported format %q", name, s.Format)
		}
		if !formatSpec.AllowsOptions && len(s.Options) > 0 {
			return fmt.Errorf("source %q %s path cannot set options", name, s.Format)
		}
	case KindObject:
		if s.Connection == "" {
			return fmt.Errorf("source %q object requires connection", name)
		}
		if s.Format != "" || len(s.Options) > 0 {
			return fmt.Errorf("source %q object cannot set format or options", name)
		}
		connection, ok := connections[s.Connection]
		if !ok {
			return fmt.Errorf("source %q references unknown connection %q", name, s.Connection)
		}
		connectionSpec, ok := LookupConnection(connection.Kind)
		if !ok || !connectionSpec.AllowsObjectSource {
			return fmt.Errorf("source %q object cannot use %s connection %q", name, connection.Kind, s.Connection)
		}
	default:
		return fmt.Errorf("source %q requires exactly one of path or object", name)
	}
	return nil
}

func sourceWithinConnectionScope(connection Connection, sourcePath string) bool {
	scope := firstNonEmpty(connection.Scope, connection.Root)
	if scope == "" {
		return true
	}
	if !IsLocalPath(scope) || !IsLocalPath(sourcePath) {
		fullPath := sourcePath
		if IsLocalPath(sourcePath) {
			fullPath = JoinScope(scope, sourcePath)
		}
		return WithinScope(scope, fullPath)
	}
	cleanScope := filepath.Clean(scope)
	cleanPath := filepath.Clean(sourcePath)
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Clean(filepath.Join(cleanScope, cleanPath))
	}
	rel, err := filepath.Rel(cleanScope, cleanPath)
	if err != nil {
		return false
	}
	return rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (c Connection) Validate(name string) (Connection, error) {
	return c.validate(name, true)
}

func (c Connection) ValidateAuthored(name string) (Connection, error) {
	provider := strings.TrimSpace(c.Credentials.Provider)
	if provider != "" && provider != "none" {
		return c, fmt.Errorf("connection %q credential references are target-owned and cannot be authored", name)
	}
	if len(c.Auth) != 0 || strings.TrimSpace(c.Host) != "" || c.Port != 0 || strings.TrimSpace(c.Database) != "" ||
		strings.TrimSpace(c.Username) != "" || strings.TrimSpace(c.SSLMode) != "" {
		return c, fmt.Errorf("connection %q endpoint, source identity, and resolved auth are target-owned and cannot be authored", name)
	}
	return c.validate(name, false)
}

func (c Connection) validate(name string, requireResolvedAuth bool) (Connection, error) {
	if err := validateSemanticIdentifier(name); err != nil {
		return c, fmt.Errorf("connection %q has invalid name: %w", name, err)
	}
	if c.Kind == "" {
		return c, fmt.Errorf("connection %q requires kind", name)
	}
	if err := validateConnectionCredentials(name, c.Kind, c.Scope, c.Credentials); err != nil {
		return c, err
	}
	connectionSpec, ok := LookupConnection(c.Kind)
	if !ok {
		return c, fmt.Errorf("connection %q has unsupported kind %q", name, c.Kind)
	}
	if c.Kind == "managed" && (strings.TrimSpace(c.Root) != "" || strings.TrimSpace(c.Scope) != "") {
		return c, fmt.Errorf("connection %q managed physical location is supplied by the active revision and cannot be authored", name)
	}
	if connectionSpec.RequiresPath {
		if c.Path == "" {
			return c, fmt.Errorf("connection %q %s requires path", name, c.Kind)
		}
	} else if c.Path != "" && !connectionSpec.AllowsPath {
		return c, fmt.Errorf("connection %q path is only supported for path-backed connections", name)
	}
	if requireResolvedAuth {
		if c.Kind == "quack" {
			if _, err := connectors.QuackURI(c.Host, c.Port); err != nil {
				return c, fmt.Errorf("connection %q quack requires endpoint: %w", name, err)
			}
			if c.SSLMode != "require" {
				return c, fmt.Errorf("connection %q quack endpoint requires sslMode require", name)
			}
			if c.Database != "" || c.Username != "" || c.Scope != "" {
				return c, fmt.Errorf("connection %q quack endpoint does not accept database, source identity, or object scope", name)
			}
		}
		auth, err := validateConnectionAuth(name, c, connectionSpec)
		if err != nil {
			return c, err
		}
		c.Auth = auth
	}
	for key := range c.Options {
		if !connectionAllowsOption(connectionSpec, key) {
			return c, fmt.Errorf("connection %q has unsupported option %q", name, key)
		}
	}
	if err := validateConnectionOptions(name, c); err != nil {
		return c, err
	}
	for key := range c.Defaults.Options {
		if err := validateSemanticIdentifier(key); err != nil {
			return c, fmt.Errorf("connection %q default option %q is invalid: %w", name, key, err)
		}
	}
	return c, nil
}

func (s Source) Role() string {
	switch s.Kind() {
	case KindPath:
		return s.Format
	case KindObject:
		return "object"
	default:
		return "source"
	}
}

func (s Source) Kind() string {
	count := 0
	kind := ""
	if s.Path != "" {
		count++
		kind = KindPath
	}
	if s.Object != "" {
		count++
		kind = KindObject
	}
	if count != 1 {
		return ""
	}
	return kind
}

func connectionAllowsOption(connection ConnectionSpec, option string) bool {
	for _, allowed := range connection.AllowedOptions {
		if option == allowed {
			return true
		}
	}
	return false
}

func validateConnectionOptions(name string, connection Connection) error {
	_ = name
	_ = connection
	return nil
}

func validateConnectionCredentials(name, kind, scope string, credentials ConnectionCredentials) error {
	if credentials.Provider == "" && credentials.Secret == "" && credentials.Region == "" && credentials.Endpoint == "" && credentials.AccountName == "" {
		return nil
	}
	if credentials.Provider == "" {
		return fmt.Errorf("connection %q credentials require provider", name)
	}
	switch credentials.Provider {
	case "none":
		if credentials.Secret != "" || credentials.Region != "" || credentials.Endpoint != "" || credentials.AccountName != "" {
			return fmt.Errorf("connection %q none credentials cannot set credential values", name)
		}
	case "env":
		if credentials.Secret == "" {
			return fmt.Errorf("connection %q env credentials require secret", name)
		}
		if credentials.Region != "" || credentials.Endpoint != "" || credentials.AccountName != "" {
			return fmt.Errorf("connection %q env credentials cannot set ambient metadata", name)
		}
	case "ambient":
		if credentials.Secret != "" {
			return fmt.Errorf("connection %q ambient credentials cannot set secret", name)
		}
		if strings.TrimSpace(scope) == "" {
			return fmt.Errorf("connection %q ambient credentials require a path scope", name)
		}
		switch kind {
		case "s3":
			if credentials.AccountName != "" {
				return fmt.Errorf("connection %q s3 ambient credentials cannot set accountName", name)
			}
		case "azure_blob":
			if strings.TrimSpace(credentials.AccountName) == "" {
				return fmt.Errorf("connection %q azure_blob ambient credentials require accountName", name)
			}
			if credentials.Region != "" || credentials.Endpoint != "" {
				return fmt.Errorf("connection %q azure_blob ambient credentials accept only accountName", name)
			}
		default:
			return fmt.Errorf("connection %q kind %q does not support ambient credentials", name, kind)
		}
	default:
		return fmt.Errorf("connection %q has unsupported credentials provider %q", name, credentials.Provider)
	}
	return nil
}

func validateConnectionAuth(name string, connection Connection, spec ConnectionSpec) (ConnectionAuth, error) {
	if len(connection.Auth) == 0 {
		if connection.Credentials.Provider == "ambient" || connection.Credentials.Provider == "env" {
			return nil, nil
		}
		if connection.Credentials.Provider == "none" && connection.Kind == "s3" {
			return nil, nil
		}
		if connection.Kind == "ducklake" && duckLakeNeedsAuth(connection) {
			return nil, fmt.Errorf("connection %q ducklake remote path requires auth", name)
		}
		if connection.Kind == "sqlite" && connection.Options["path"] != nil {
			return nil, nil
		}
		if spec.AllowNoAuth {
			return nil, nil
		}
		return nil, fmt.Errorf("connection %q %s requires auth", name, connection.Kind)
	}
	resolved := make(ConnectionAuth, len(connection.Auth))
	for key, value := range connection.Auth {
		if err := validateSemanticIdentifier(key); err != nil {
			return nil, fmt.Errorf("connection %q auth key %q is invalid: %w", name, key, err)
		}
		if !connectionAllowsAuthKey(spec, key) {
			return nil, fmt.Errorf("connection %q has unsupported auth key %q", name, key)
		}
		resolved[key] = value
	}
	if !connectionHasRequiredAuth(resolved, spec.RequiredAuthSets) {
		return nil, fmt.Errorf("connection %q %s auth is missing required credentials", name, connection.Kind)
	}
	return resolved, nil
}

func ResolveConnectionAuth(connection Connection) (ConnectionAuth, error) {
	if len(connection.Auth) > 0 {
		resolved := make(ConnectionAuth, len(connection.Auth))
		for key, value := range connection.Auth {
			resolved[key] = value
		}
		return resolved, nil
	}
	if connection.Credentials.Provider == "ambient" {
		auth := ConnectionAuth{}
		if connection.Credentials.Region != "" {
			auth["region"] = connection.Credentials.Region
		}
		if connection.Credentials.Endpoint != "" {
			auth["endpoint"] = connection.Credentials.Endpoint
		}
		if connection.Credentials.AccountName != "" {
			auth["account_name"] = connection.Credentials.AccountName
		}
		return auth, nil
	}
	return nil, nil
}

func ConnectionCredentialsConfigured(connection Connection) bool {
	return len(connection.Auth) > 0 || connection.Credentials.Provider != "" && connection.Credentials.Provider != "none"
}

func connectionAllowsAuthKey(connection ConnectionSpec, key string) bool {
	for _, allowed := range connection.AuthKeys {
		if key == allowed {
			return true
		}
	}
	return false
}

func connectionHasRequiredAuth(auth ConnectionAuth, requiredSets [][]string) bool {
	if len(requiredSets) == 0 {
		return true
	}
	for _, required := range requiredSets {
		missing := false
		for _, key := range required {
			value, ok := auth[key]
			if !ok || fmt.Sprint(value) == "" {
				missing = true
				break
			}
		}
		if !missing {
			return true
		}
	}
	return false
}

func duckLakeNeedsAuth(connection Connection) bool {
	if connection.Scope != "" && !IsLocalPath(connection.Scope) {
		return true
	}
	if connection.Path != "" && !IsLocalPath(connection.Path) {
		return true
	}
	if dataPath, ok := connection.Options["data_path"]; ok && !IsLocalPath(fmt.Sprint(dataPath)) {
		return true
	}
	return false
}

func validateSemanticIdentifier(value string) error {
	if !semanticIdentifierPattern.MatchString(value) {
		return fmt.Errorf("must match %s", semanticIdentifierPattern.String())
	}
	return nil
}

func (m *Model) TableNames() []string {
	names := make([]string, 0, len(m.Tables))
	for name := range m.Tables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
