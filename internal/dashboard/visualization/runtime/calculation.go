package runtime

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"

	visualizationdecimal "github.com/flidai/leapview/internal/dashboard/visualization/decimal"
	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// ApplyVisualCalculations evaluates the compiler-owned, closed calculation
// plan against one governed result frame. Evaluation order is dependency
// driven; output column order remains the authored order in the immutable
// specification.
func ApplyVisualCalculations(base ir.VisualizationSpecBase, datasetID string, frame Frame, completeness ir.VisualizationCompleteness) (Frame, []ir.VisualizationDiagnostic, error) {
	calculations := calculationsForDataset(base.Calculations, datasetID)
	if len(calculations) == 0 {
		return cloneFrame(frame), nil, nil
	}
	schema, err := compiledDatasetSchema(base, datasetID)
	if err != nil {
		return Frame{}, nil, err
	}
	columnIndex := make(map[string]int, len(frame.Columns))
	for index, column := range frame.Columns {
		if _, exists := columnIndex[column]; exists {
			return Frame{}, nil, fmt.Errorf("visual calculation dataset %q has duplicate frame column %q", datasetID, column)
		}
		columnIndex[column] = index
	}
	for _, field := range schema.Fields {
		if _, ok := columnIndex[field.ID]; !ok && !calculationHasID(calculations, field.ID) {
			return Frame{}, nil, fmt.Errorf("visual calculation dataset %q is missing source field %q", datasetID, field.ID)
		}
	}

	order, err := calculationEvaluationOrder(datasetID, calculations, columnIndex)
	if err != nil {
		return Frame{}, nil, err
	}
	values := make(map[string][]any, len(calculations))
	for _, calculationIndex := range order {
		calculation := calculations[calculationIndex]
		decimalOutput := false
		for _, field := range schema.Fields {
			if field.ID == calculation.ID {
				decimalOutput = field.DataType == ir.VisualizationDataTypeDecimal
				break
			}
		}
		result, evalErr := evaluateCalculation(datasetID, calculation, frame, columnIndex, values, decimalOutput)
		if evalErr != nil {
			return Frame{}, nil, fmt.Errorf("visual calculation %q: %w", calculation.ID, evalErr)
		}
		values[calculation.ID] = result
	}

	result := Frame{
		Columns:      make([]string, 0, len(frame.Columns)+len(calculations)),
		Rows:         make([][]any, len(frame.Rows)),
		Completeness: frame.Completeness,
	}
	result.Columns = append(result.Columns, frame.Columns...)
	for _, calculation := range calculations {
		result.Columns = append(result.Columns, calculation.ID)
	}
	for rowIndex, sourceRow := range frame.Rows {
		result.Rows[rowIndex] = make([]any, len(result.Columns))
		copy(result.Rows[rowIndex], sourceRow)
		for calculationIndex, calculation := range calculations {
			result.Rows[rowIndex][len(frame.Columns)+calculationIndex] = values[calculation.ID][rowIndex]
		}
	}
	var diagnostics []ir.VisualizationDiagnostic
	if completeness == ir.VisualizationCompletenessPartial || completeness == ir.VisualizationCompletenessTruncated {
		diagnostics = append(diagnostics, ir.VisualizationDiagnostic{
			Code: "visual_calculation_incomplete_frame", Severity: ir.VisualizationDiagnosticSeverityWarning,
			Message: "Visual calculations were evaluated over an incomplete visible frame; totals, ranks, windows, and comparisons may exclude unavailable rows.",
		})
	}
	return result, diagnostics, nil
}

func calculationsForDataset(all *[]ir.VisualizationCalculation, datasetID string) []ir.VisualizationCalculation {
	if all == nil {
		return nil
	}
	out := make([]ir.VisualizationCalculation, 0, len(*all))
	for _, calculation := range *all {
		if calculation.Dataset == datasetID {
			out = append(out, calculation)
		}
	}
	return out
}

func cloneFrame(frame Frame) Frame {
	out := Frame{Columns: append([]string{}, frame.Columns...), Rows: make([][]any, len(frame.Rows)), Completeness: frame.Completeness}
	for index := range frame.Rows {
		out.Rows[index] = append([]any{}, frame.Rows[index]...)
	}
	return out
}

func calculationHasID(calculations []ir.VisualizationCalculation, id string) bool {
	for _, calculation := range calculations {
		if calculation.ID == id {
			return true
		}
	}
	return false
}

func calculationEvaluationOrder(datasetID string, calculations []ir.VisualizationCalculation, columns map[string]int) ([]int, error) {
	indexByID := make(map[string]int, len(calculations))
	for index, calculation := range calculations {
		if calculation.ID == "" {
			return nil, fmt.Errorf("visual calculation %d has an empty ID", index)
		}
		if _, exists := columns[calculation.ID]; exists {
			return nil, fmt.Errorf("visual calculation %q collides with a compiled field", calculation.ID)
		}
		if _, exists := indexByID[calculation.ID]; exists {
			return nil, fmt.Errorf("duplicate visual calculation %q", calculation.ID)
		}
		indexByID[calculation.ID] = index
	}
	state := make([]uint8, len(calculations))
	order := make([]int, 0, len(calculations))
	var visit func(int) error
	visit = func(index int) error {
		switch state[index] {
		case 1:
			return fmt.Errorf("visual calculation dependency cycle includes %q", calculations[index].ID)
		case 2:
			return nil
		}
		state[index] = 1
		calculation := calculations[index]
		refs := []ir.VisualizationFieldRef{calculation.Source}
		for _, order := range calculation.OrderBy {
			refs = append(refs, order.Field)
		}
		refs = append(refs, calculation.PartitionBy...)
		if calculation.Parent != nil {
			refs = append(refs, *calculation.Parent)
		}
		if calculation.Lookup != nil {
			refs = append(refs, calculation.Lookup.Field)
		}
		for _, ref := range refs {
			if ref.Dataset != datasetID {
				return fmt.Errorf("visual calculation %q references dataset %q, want %q", calculation.ID, ref.Dataset, datasetID)
			}
			if dependency, ok := indexByID[ref.Field]; ok {
				if err := visit(dependency); err != nil {
					return err
				}
				continue
			}
			if _, ok := columns[ref.Field]; !ok {
				return fmt.Errorf("visual calculation %q references unknown field %q", calculation.ID, ref.Field)
			}
		}
		state[index] = 2
		order = append(order, index)
		return nil
	}
	for index := range calculations {
		if err := visit(index); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func evaluateCalculation(datasetID string, calculation ir.VisualizationCalculation, frame Frame, columns map[string]int, calculated map[string][]any, decimalOutput bool) ([]any, error) {
	if calculation.Source.Dataset != datasetID {
		return nil, fmt.Errorf("source dataset is %q, want %q", calculation.Source.Dataset, datasetID)
	}
	switch calculation.Axis {
	case ir.VisualizationCalculationAxisRows, ir.VisualizationCalculationAxisColumns, ir.VisualizationCalculationAxisHierarchy, ir.VisualizationCalculationAxisFacets:
	default:
		return nil, fmt.Errorf("unsupported axis %q", calculation.Axis)
	}
	source, err := calculationValues(calculation.Source.Field, frame, columns, calculated)
	if err != nil {
		return nil, err
	}
	partitions, err := calculationPartitions(calculation, frame, columns, calculated)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(frame.Rows))
	for _, members := range partitions {
		ordered, orderErr := orderCalculationRows(calculation, members, frame, columns, calculated)
		if orderErr != nil {
			return nil, orderErr
		}
		switch calculation.Template {
		case ir.VisualizationCalculationTemplateRunningTotal:
			evaluateRunningTotal(out, source, ordered, decimalOutput)
		case ir.VisualizationCalculationTemplateMovingAverage:
			if calculation.Window == nil || *calculation.Window <= 0 {
				return nil, fmt.Errorf("moving_average requires a positive window")
			}
			evaluateMovingAverage(out, source, ordered, int(*calculation.Window), decimalOutput)
		case ir.VisualizationCalculationTemplateDifference:
			evaluateDifference(out, source, ordered, calculationOffset(calculation), false, decimalOutput)
		case ir.VisualizationCalculationTemplatePercentageDifference:
			evaluateDifference(out, source, ordered, calculationOffset(calculation), true, decimalOutput)
		case ir.VisualizationCalculationTemplatePercentOfParent:
			evaluateShare(out, source, ordered, decimalOutput)
		case ir.VisualizationCalculationTemplatePercentOfGrandTotal:
			evaluateShare(out, source, ordered, decimalOutput)
		case ir.VisualizationCalculationTemplateRank:
			evaluateRank(out, source, ordered)
		case ir.VisualizationCalculationTemplateCumulativeContribution:
			evaluateCumulativeContribution(out, source, ordered, decimalOutput)
		case ir.VisualizationCalculationTemplateLookup:
			if calculation.Lookup == nil {
				return nil, fmt.Errorf("lookup requires a match field and value")
			}
			if err := evaluateLookup(out, source, ordered, *calculation.Lookup, frame, columns, calculated); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported template %q", calculation.Template)
		}
	}
	return out, nil
}

func calculationValues(field string, frame Frame, columns map[string]int, calculated map[string][]any) ([]any, error) {
	if values, ok := calculated[field]; ok {
		return values, nil
	}
	index, ok := columns[field]
	if !ok {
		return nil, fmt.Errorf("unknown field %q", field)
	}
	values := make([]any, len(frame.Rows))
	for rowIndex, row := range frame.Rows {
		if index >= len(row) {
			return nil, fmt.Errorf("row %d omits field %q", rowIndex, field)
		}
		values[rowIndex] = row[index]
	}
	return values, nil
}

func calculationPartitions(calculation ir.VisualizationCalculation, frame Frame, columns map[string]int, calculated map[string][]any) ([][]int, error) {
	if len(calculation.PartitionBy) == 0 && calculation.Parent == nil {
		members := make([]int, len(frame.Rows))
		for index := range frame.Rows {
			members[index] = index
		}
		return [][]int{members}, nil
	}
	fields := append([]ir.VisualizationFieldRef{}, calculation.PartitionBy...)
	if calculation.Parent != nil {
		fields = append(fields, *calculation.Parent)
	}
	fieldValues := make([][]any, len(fields))
	for index, field := range fields {
		values, err := calculationValues(field.Field, frame, columns, calculated)
		if err != nil {
			return nil, err
		}
		fieldValues[index] = values
	}
	if len(fields) == 1 {
		byKey := map[calculationPartitionKey][]int{}
		order := []calculationPartitionKey{}
		for rowIndex := range frame.Rows {
			key := typedCalculationPartitionKey(fieldValues[0][rowIndex])
			if _, exists := byKey[key]; !exists {
				order = append(order, key)
			}
			byKey[key] = append(byKey[key], rowIndex)
		}
		out := make([][]int, 0, len(order))
		for _, key := range order {
			out = append(out, byKey[key])
		}
		return out, nil
	}
	byKey := map[string][]int{}
	order := []string{}
	for rowIndex := range frame.Rows {
		var key strings.Builder
		for fieldIndex := range fields {
			key.WriteString(stableCalculationKey(fieldValues[fieldIndex][rowIndex]))
			key.WriteByte(0)
		}
		value := key.String()
		if _, exists := byKey[value]; !exists {
			order = append(order, value)
		}
		byKey[value] = append(byKey[value], rowIndex)
	}
	out := make([][]int, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key])
	}
	return out, nil
}

type calculationPartitionKey struct {
	kind  uint8
	text  string
	value bool
}

func typedCalculationPartitionKey(value any) calculationPartitionKey {
	switch typed := value.(type) {
	case nil:
		return calculationPartitionKey{}
	case string:
		return calculationPartitionKey{kind: 1, text: typed}
	case bool:
		return calculationPartitionKey{kind: 2, value: typed}
	default:
		if numberKey, ok := calculationNumericKey(value); ok {
			return calculationPartitionKey{kind: 3, text: numberKey}
		}
		return calculationPartitionKey{kind: 4, text: fmt.Sprintf("%T:%v", value, value)}
	}
}

func stableCalculationKey(value any) string {
	switch typed := value.(type) {
	case nil:
		return "n:"
	case string:
		return "s:" + typed
	case bool:
		return "b:" + strconv.FormatBool(typed)
	default:
		if numberKey, ok := calculationNumericKey(value); ok {
			return "n:" + numberKey
		}
		return fmt.Sprintf("%T:%v", value, value)
	}
}

func calculationNumericKey(value any) (string, bool) {
	var number float64
	switch typed := value.(type) {
	case int:
		return strconv.FormatInt(int64(typed), 10), true
	case int8:
		return strconv.FormatInt(int64(typed), 10), true
	case int16:
		return strconv.FormatInt(int64(typed), 10), true
	case int32:
		return strconv.FormatInt(int64(typed), 10), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case uint:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint8:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint16:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint32:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint64:
		return strconv.FormatUint(typed, 10), true
	case float32:
		number = float64(typed)
	case float64:
		number = typed
	default:
		return "", false
	}
	if math.IsNaN(number) {
		return "", false
	}
	if number == 0 {
		return "0", true
	}
	return strconv.FormatFloat(number, 'g', -1, 64), true
}

func orderCalculationRows(calculation ir.VisualizationCalculation, members []int, frame Frame, columns map[string]int, calculated map[string][]any) ([]int, error) {
	ordered := append([]int{}, members...)
	if len(calculation.OrderBy) == 0 {
		return ordered, nil
	}
	orderValues := make([][]any, len(calculation.OrderBy))
	for index, order := range calculation.OrderBy {
		values, err := calculationValues(order.Field.Field, frame, columns, calculated)
		if err != nil {
			return nil, err
		}
		orderValues[index] = values
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		for orderIndex, order := range calculation.OrderBy {
			leftValue, rightValue := orderValues[orderIndex][left], orderValues[orderIndex][right]
			if leftValue == nil || rightValue == nil {
				if leftValue == nil && rightValue != nil {
					return false
				}
				if leftValue != nil {
					return true
				}
				continue
			}
			comparison := compareCalculationValues(leftValue, rightValue)
			if comparison == 0 {
				continue
			}
			if order.Direction == ir.VisualizationSortDirectionDescending {
				return comparison > 0
			}
			return comparison < 0
		}
		return left < right
	})
	return ordered, nil
}

func compareCalculationValues(left, right any) int {
	leftDecimal, _, leftExact := calculationDecimal(left)
	rightDecimal, _, rightExact := calculationDecimal(right)
	if leftExact && rightExact {
		return leftDecimal.Cmp(rightDecimal)
	}
	leftNumber, leftNumeric := calculationNumber(left)
	rightNumber, rightNumeric := calculationNumber(right)
	if leftNumeric && rightNumeric {
		switch {
		case leftNumber < rightNumber:
			return -1
		case leftNumber > rightNumber:
			return 1
		default:
			return 0
		}
	}
	leftText, rightText := fmt.Sprint(left), fmt.Sprint(right)
	return strings.Compare(leftText, rightText)
}

func equalCalculationValues(left, right any) bool {
	leftDecimal, _, leftExact := calculationDecimal(left)
	rightDecimal, _, rightExact := calculationDecimal(right)
	if leftExact && rightExact {
		return leftDecimal.Cmp(rightDecimal) == 0
	}
	return fmt.Sprint(left) == fmt.Sprint(right)
}

func evaluateRunningTotal(out, source []any, ordered []int, decimalOutput bool) {
	if decimalOutput {
		total := new(big.Rat)
		scale := 0
		for _, rowIndex := range ordered {
			value, valueScale, ok := calculationDecimal(source[rowIndex])
			if !ok {
				out[rowIndex] = nil
				continue
			}
			total.Add(total, value)
			if valueScale > scale {
				scale = valueScale
			}
			out[rowIndex] = decimalCalculationString(total, scale)
		}
		return
	}
	total := 0.0
	for _, rowIndex := range ordered {
		value, ok := calculationNumber(source[rowIndex])
		if !ok {
			out[rowIndex] = nil
			continue
		}
		total += value
		out[rowIndex] = total
	}
}

func evaluateMovingAverage(out, source []any, ordered []int, window int, decimalOutput bool) {
	for position, rowIndex := range ordered {
		start := max(0, position-window+1)
		if decimalOutput {
			total := new(big.Rat)
			count, scale := 0, 0
			for _, candidate := range ordered[start : position+1] {
				value, valueScale, ok := calculationDecimal(source[candidate])
				if !ok {
					continue
				}
				total.Add(total, value)
				count++
				if valueScale > scale {
					scale = valueScale
				}
			}
			if count == 0 {
				out[rowIndex] = nil
			} else {
				out[rowIndex] = decimalCalculationString(new(big.Rat).Quo(total, new(big.Rat).SetInt64(int64(count))), max(scale, 18))
			}
			continue
		}
		total, count := 0.0, 0
		for _, candidate := range ordered[start : position+1] {
			if value, ok := calculationNumber(source[candidate]); ok {
				total += value
				count++
			}
		}
		if count == 0 {
			out[rowIndex] = nil
		} else {
			out[rowIndex] = total / float64(count)
		}
	}
}

func calculationOffset(calculation ir.VisualizationCalculation) int {
	if calculation.Offset == nil {
		return 1
	}
	return int(*calculation.Offset)
}

func evaluateDifference(out, source []any, ordered []int, offset int, percentage, decimalOutput bool) {
	if offset <= 0 {
		return
	}
	for position, rowIndex := range ordered {
		previous := position - offset
		if previous < 0 {
			out[rowIndex] = nil
			continue
		}
		if decimalOutput {
			current, currentScale, currentOK := calculationDecimal(source[rowIndex])
			previousValue, previousScale, previousOK := calculationDecimal(source[ordered[previous]])
			if !currentOK || !previousOK || percentage && previousValue.Sign() == 0 {
				out[rowIndex] = nil
				continue
			}
			difference := new(big.Rat).Sub(current, previousValue)
			if percentage {
				difference.Quo(difference, new(big.Rat).Abs(previousValue))
				out[rowIndex] = decimalCalculationString(difference, 18)
			} else {
				out[rowIndex] = decimalCalculationString(difference, max(currentScale, previousScale))
			}
			continue
		}
		currentValue, currentOK := calculationNumber(source[rowIndex])
		previousValue, previousOK := calculationNumber(source[ordered[previous]])
		if !currentOK || !previousOK || percentage && previousValue == 0 {
			out[rowIndex] = nil
			continue
		}
		difference := currentValue - previousValue
		if percentage {
			out[rowIndex] = difference / math.Abs(previousValue)
		} else {
			out[rowIndex] = difference
		}
	}
}

func evaluateShare(out, source []any, ordered []int, decimalOutput bool) {
	if decimalOutput {
		total := new(big.Rat)
		for _, rowIndex := range ordered {
			if value, _, ok := calculationDecimal(source[rowIndex]); ok {
				total.Add(total, value)
			}
		}
		for _, rowIndex := range ordered {
			value, _, ok := calculationDecimal(source[rowIndex])
			if !ok || total.Sign() == 0 {
				out[rowIndex] = nil
			} else {
				out[rowIndex] = decimalCalculationString(new(big.Rat).Quo(value, total), 18)
			}
		}
		return
	}
	total := 0.0
	for _, rowIndex := range ordered {
		if value, ok := calculationNumber(source[rowIndex]); ok {
			total += value
		}
	}
	for _, rowIndex := range ordered {
		value, ok := calculationNumber(source[rowIndex])
		if !ok || total == 0 {
			out[rowIndex] = nil
		} else {
			out[rowIndex] = value / total
		}
	}
}

func evaluateRank(out, source []any, ordered []int) {
	rank := 0
	var previous any
	havePrevious := false
	for _, rowIndex := range ordered {
		value := source[rowIndex]
		_, ok := calculationNumber(value)
		if !ok {
			out[rowIndex] = nil
			continue
		}
		if !havePrevious || compareCalculationValues(value, previous) != 0 {
			rank++
			previous = value
			havePrevious = true
		}
		out[rowIndex] = int64(rank)
	}
}

func evaluateCumulativeContribution(out, source []any, ordered []int, decimalOutput bool) {
	if decimalOutput {
		total := new(big.Rat)
		for _, rowIndex := range ordered {
			if value, _, ok := calculationDecimal(source[rowIndex]); ok {
				total.Add(total, value)
			}
		}
		running := new(big.Rat)
		for _, rowIndex := range ordered {
			value, _, ok := calculationDecimal(source[rowIndex])
			if !ok || total.Sign() == 0 {
				out[rowIndex] = nil
				continue
			}
			running.Add(running, value)
			out[rowIndex] = decimalCalculationString(new(big.Rat).Quo(running, total), 18)
		}
		return
	}
	total := 0.0
	for _, rowIndex := range ordered {
		if value, ok := calculationNumber(source[rowIndex]); ok {
			total += value
		}
	}
	running := 0.0
	for _, rowIndex := range ordered {
		value, ok := calculationNumber(source[rowIndex])
		if !ok || total == 0 {
			out[rowIndex] = nil
			continue
		}
		running += value
		out[rowIndex] = running / total
	}
}

func evaluateLookup(out, source []any, ordered []int, lookup ir.VisualizationCalculationLookup, frame Frame, columns map[string]int, calculated map[string][]any) error {
	values, err := calculationValues(lookup.Field.Field, frame, columns, calculated)
	if err != nil {
		return err
	}
	match := -1
	for _, rowIndex := range ordered {
		if !equalCalculationValues(values[rowIndex], lookup.Value) {
			continue
		}
		if match >= 0 {
			return fmt.Errorf("lookup field %q value %q is ambiguous within a partition", lookup.Field.Field, lookup.Value)
		}
		match = rowIndex
	}
	var value any
	if match >= 0 {
		value = source[match]
	}
	for _, rowIndex := range ordered {
		out[rowIndex] = value
	}
	return nil
}

func calculationNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), !math.IsNaN(float64(typed))
	case float64:
		return typed, !math.IsNaN(typed)
	case string:
		number, err := strconv.ParseFloat(typed, 64)
		return number, err == nil && !math.IsNaN(number)
	default:
		return 0, false
	}
}

func calculationDecimal(value any) (*big.Rat, int, bool) {
	if text, ok := value.(string); ok {
		rational, scale, err := visualizationdecimal.Parse(text)
		return rational, scale, err == nil
	}
	switch typed := value.(type) {
	case int:
		return new(big.Rat).SetInt64(int64(typed)), 0, true
	case int8:
		return new(big.Rat).SetInt64(int64(typed)), 0, true
	case int16:
		return new(big.Rat).SetInt64(int64(typed)), 0, true
	case int32:
		return new(big.Rat).SetInt64(int64(typed)), 0, true
	case int64:
		return new(big.Rat).SetInt64(typed), 0, true
	case uint:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(typed))), 0, true
	case uint8:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(typed))), 0, true
	case uint16:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(typed))), 0, true
	case uint32:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(typed))), 0, true
	case uint64:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(typed)), 0, true
	default:
		return nil, 0, false
	}
}

func decimalCalculationString(value *big.Rat, scale int) string {
	if value == nil || value.Sign() == 0 {
		return "0"
	}
	if scale < 0 {
		scale = 0
	}
	return value.FloatString(scale)
}
