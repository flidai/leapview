package contractprojection

import (
	"errors"
	"reflect"
	"unicode/utf8"
)

// validateInputEncoding runs before encoding/json can replace invalid UTF-8
// with U+FFFD. This traversal validates input only: generated DTOs and explicit
// projection functions, never reflection, determine the projection wire shape.
func validateInputEncoding(input any) error {
	var visit func(reflect.Value, int) error
	visit = func(value reflect.Value, depth int) error {
		if depth > 256 {
			return errors.New("authored input nesting exceeds validation bound")
		}
		if !value.IsValid() {
			return nil
		}
		switch value.Kind() {
		case reflect.String:
			if !utf8.ValidString(value.String()) {
				return errors.New("authored input contains invalid UTF-8")
			}
		case reflect.Interface, reflect.Pointer:
			if !value.IsNil() {
				return visit(value.Elem(), depth+1)
			}
		case reflect.Struct:
			for i := 0; i < value.NumField(); i++ {
				if value.Type().Field(i).IsExported() {
					if err := visit(value.Field(i), depth+1); err != nil {
						return err
					}
				}
			}
		case reflect.Map:
			iterator := value.MapRange()
			for iterator.Next() {
				if err := visit(iterator.Key(), depth+1); err != nil {
					return err
				}
				if err := visit(iterator.Value(), depth+1); err != nil {
					return err
				}
			}
		case reflect.Array, reflect.Slice:
			for i := 0; i < value.Len(); i++ {
				if err := visit(value.Index(i), depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(reflect.ValueOf(input), 0)
}
