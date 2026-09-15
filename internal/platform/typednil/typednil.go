// Package typednil provides the small reflection boundary needed when
// interfaces carry nil-capable concrete values.
package typednil

import "reflect"

// IsNil reports whether value is nil, including an interface containing a
// nil channel, function, interface, map, pointer, slice, or unsafe pointer.
// Scalar values are never nil and are handled without calling reflect.Value.IsNil
// on an invalid kind.
func IsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return reflected.IsNil()
	default:
		return false
	}
}
