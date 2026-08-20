package duckdbsql

import (
	"bytes"
	"encoding/json"
)

func (d *decoder) value(raw json.RawMessage) (Value, error) {
	if len(raw) == 0 || isNull(raw) {
		return Value{Kind: ValueNull}, nil
	}
	if raw[0] == '{' {
		m, err := d.object(raw)
		if err != nil {
			return Value{}, err
		}
		// Serialized constants carry is_null and/or value alongside their type.
		// Other supporting objects (for example DECIMAL_TYPE_INFO) also have a
		// discriminator named type and must remain typed Value objects rather
		// than being decoded as constants.
		_, hasNullMarker := m["is_null"]
		_, hasConstantValue := m["value"]
		if hasNullMarker || hasConstantValue {
			return d.constantValue(m)
		}
		if len(m) > d.limits.MaxArrayItems {
			return Value{}, limitError("value object field count exceeds parser limit")
		}
		out := Value{Kind: ValueObject}
		for k, v := range m {
			x, er := d.value(v)
			if er != nil {
				return Value{}, er
			}
			out.Object = append(out.Object, Field{Name: k, Value: x})
		}
		return out, nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return Value{}, malformedError("invalid value", err)
	}
	switch x := v.(type) {
	case nil:
		return Value{Kind: ValueNull}, nil
	case bool:
		return Value{Kind: ValueBool, Bool: x}, nil
	case string:
		return Value{Kind: ValueString, String: x}, nil
	case json.Number:
		return Value{Kind: ValueNumber, Number: x.String()}, nil
	case []any:
		if len(x) > d.limits.MaxArrayItems {
			return Value{}, limitError("value array item count exceeds parser limit")
		}
		out := Value{Kind: ValueArray}
		for _, e := range x {
			b, _ := json.Marshal(e)
			q, er := d.value(b)
			if er != nil {
				return Value{}, er
			}
			out.Array = append(out.Array, q)
		}
		return out, nil
	default:
		return Value{}, compatibilityError("unsupported DuckDB value")
	}
}
func (d *decoder) constantValue(m map[string]json.RawMessage) (Value, error) {
	if err := checkKeys(m, "type", "is_null", "value"); err != nil {
		return Value{}, err
	}
	raw, ok := m["value"]
	if !ok || isNull(raw) {
		return Value{Kind: ValueNull}, nil
	}
	return d.value(raw)
}
func (d *decoder) logicalTypeValue(raw json.RawMessage) (LogicalType, error) {
	m, err := d.object(raw)
	if err != nil {
		return LogicalType{}, err
	}
	if err = checkKeys(m, "id", "type_modifiers", "type_info"); err != nil {
		return LogicalType{}, err
	}
	id, err := requiredString(m, "id")
	if err != nil {
		return LogicalType{}, err
	}
	if !knownLogicalType(id) {
		return LogicalType{}, compatibilityError("unknown logical type " + id)
	}
	l := LogicalType{ID: id}
	if raw, ok := m["type_modifiers"]; ok {
		arr, e := rawArray(raw)
		if e != nil {
			return LogicalType{}, e
		}
		if len(arr) > d.limits.MaxArrayItems {
			return LogicalType{}, limitError("logical type modifier count exceeds parser limit")
		}
		for _, v := range arr {
			i, e := int64Value(v)
			if e != nil {
				return LogicalType{}, e
			}
			l.Modifiers = append(l.Modifiers, i)
		}
	}
	if raw, ok := m["type_info"]; ok && !isNull(raw) {
		l.Info, err = d.value(raw)
		if err != nil {
			return LogicalType{}, err
		}
	}
	return l, nil
}
func (d *decoder) logicalTypes(raw json.RawMessage) ([]LogicalType, error) {
	arr, err := rawArray(raw)
	if err != nil {
		return nil, err
	}
	if len(arr) > d.limits.MaxArrayItems {
		return nil, limitError("logical type count exceeds parser limit")
	}
	out := make([]LogicalType, 0, len(arr))
	for _, v := range arr {
		x, e := d.logicalTypeValue(v)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, nil
}

func (v Value) objectField(name string) (Value, bool) {
	for _, f := range v.Object {
		if f.Name == name {
			return f.Value, true
		}
	}
	return Value{}, false
}
func (d *decoder) values(raw json.RawMessage) ([]Value, error) {
	a, e := rawArray(raw)
	if e != nil {
		return nil, e
	}
	if len(a) > d.limits.MaxArrayItems {
		return nil, limitError("value count exceeds parser limit")
	}
	out := make([]Value, 0, len(a))
	for _, v := range a {
		x, e := d.value(v)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, nil
}
