// Package chi defines the supported Chi runtime boundary for generated servers.
package chi

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"time"

	chi "github.com/go-chi/chi/v5"
)

// Router is the supported routing surface for generated APIGen servers.
type Router = chi.Router

// Route captures one generated HTTP route.
type Route struct {
	Method      string
	Path        string
	OperationID string
}

// RegisterRoutes mounts generated routes on the provided router.
func RegisterRoutes(router Router, routes []Route, handle func(operationID string, w http.ResponseWriter, r *http.Request)) {
	for _, route := range routes {
		operationID := route.OperationID
		router.MethodFunc(route.Method, route.Path, func(w http.ResponseWriter, r *http.Request) {
			handle(operationID, w, r)
		})
	}
}

// URLParam resolves a Chi path parameter by name.
func URLParam(r *http.Request, key string) string {
	return chi.URLParam(r, key)
}

var timeType = reflect.TypeOf(time.Time{})

// BindPathParameter decodes one path parameter into the provided destination.
func BindPathParameter(name string, value string, required bool, dest any) error {
	if value == "" {
		if required {
			return fmt.Errorf("missing required path parameter %q", name)
		}
		return nil
	}

	if err := bindParameterValues(dest, []string{value}); err != nil {
		return fmt.Errorf("invalid path parameter %q: %w", name, err)
	}

	return nil
}

// BindQueryParameter decodes one query parameter into the provided destination.
func BindQueryParameter(values url.Values, name string, required bool, dest any) error {
	rawValues, ok := values[name]
	if !ok || len(rawValues) == 0 {
		if required {
			return fmt.Errorf("missing required query parameter %q", name)
		}
		return nil
	}

	if err := bindParameterValues(dest, rawValues); err != nil {
		return fmt.Errorf("invalid query parameter %q: %w", name, err)
	}

	return nil
}

// BindHeaderParameter decodes one HTTP header into the provided destination.
func BindHeaderParameter(values http.Header, name string, required bool, dest any) error {
	rawValues, ok := values[http.CanonicalHeaderKey(name)]
	if !ok || len(rawValues) == 0 {
		rawValues = values[name]
	}
	if len(rawValues) == 0 {
		if required {
			return fmt.Errorf("missing required header parameter %q", name)
		}
		return nil
	}

	if err := bindParameterValues(dest, rawValues); err != nil {
		return fmt.Errorf("invalid header parameter %q: %w", name, err)
	}

	return nil
}

// SafeIntToInt32 converts an int to int32 while clamping to the int32 range.
func SafeIntToInt32(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

func bindParameterValues(dest any, rawValues []string) error {
	destValue := reflect.ValueOf(dest)
	if !destValue.IsValid() || destValue.Kind() != reflect.Ptr || destValue.IsNil() {
		return fmt.Errorf("destination must be a non-nil pointer")
	}
	if len(rawValues) == 0 {
		return nil
	}

	return assignBoundValues(destValue.Elem(), rawValues)
}

func assignBoundValues(target reflect.Value, rawValues []string) error {
	if target.Kind() == reflect.Ptr {
		bound := reflect.New(target.Type().Elem())
		if err := assignBoundValues(bound.Elem(), rawValues); err != nil {
			return err
		}
		target.Set(bound)
		return nil
	}

	if target.Kind() == reflect.Slice {
		slice := reflect.MakeSlice(target.Type(), 0, len(rawValues))
		for _, raw := range rawValues {
			item := reflect.New(target.Type().Elem()).Elem()
			if err := assignBoundScalar(item, raw); err != nil {
				return err
			}
			slice = reflect.Append(slice, item)
		}
		target.Set(slice)
		return nil
	}

	return assignBoundScalar(target, rawValues[0])
}

func assignBoundScalar(target reflect.Value, raw string) error {
	if target.Type() == timeType {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return err
		}
		target.Set(reflect.ValueOf(parsed))
		return nil
	}

	switch target.Kind() {
	case reflect.String:
		target.SetString(raw)
		return nil
	case reflect.Bool:
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		target.SetBool(parsed)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		parsed, err := strconv.ParseInt(raw, 10, target.Type().Bits())
		if err != nil {
			return err
		}
		target.SetInt(parsed)
		return nil
	default:
		return fmt.Errorf("unsupported destination type %s", target.Type())
	}
}
