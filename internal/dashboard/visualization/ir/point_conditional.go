package ir

import (
	"fmt"
	"sort"
)

func validatePointMarkFillColors(rule VisualizationConditionalRule) error {
	requireColor := func(position string, style VisualizationConditionalStyle) error {
		if style.Color == nil {
			return fmt.Errorf("point mark_fill %s style requires color", position)
		}
		return nil
	}
	switch value := rule.Value.(type) {
	case *GradientVisualizationConditionalRule:
		for _, named := range []struct {
			position string
			style    VisualizationConditionalStyle
		}{{"low", value.Low}, {"high", value.High}, {"null", value.NullStyle}} {
			if err := requireColor(named.position, named.style); err != nil {
				return err
			}
		}
	case *RulesVisualizationConditionalRule:
		for index, threshold := range value.Rules {
			if err := requireColor(fmt.Sprintf("rule %d", index), threshold.Style); err != nil {
				return err
			}
		}
		if err := requireColor("null", value.NullStyle); err != nil {
			return err
		}
		return requireColor("default", value.DefaultStyle)
	case *FieldVisualizationConditionalRule:
		values := make([]string, 0, len(value.Values))
		for fieldValue := range value.Values {
			values = append(values, fieldValue)
		}
		sort.Strings(values)
		for _, fieldValue := range values {
			if err := requireColor(fmt.Sprintf("value %q", fieldValue), value.Values[fieldValue]); err != nil {
				return err
			}
		}
		if err := requireColor("null", value.NullStyle); err != nil {
			return err
		}
		return requireColor("default", value.DefaultStyle)
	}
	return nil
}
