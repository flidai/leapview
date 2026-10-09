package credential

import (
	"encoding/json"
	"github.com/flidai/leapview/internal/analytics/connectors"
	"github.com/flidai/leapview/internal/credential/encryption"
	"slices"
	"unicode/utf8"
)

func encodeFields(scope Scope, fields map[string]string) ([]byte, error) {
	if scope.Resource.ScopeKind == "agent" && scope.Provider == "agent-disabled" {
		if !disabledAgentCredential(scope, fields) {
			return nil, ErrInvalid
		}
		return json.Marshal(fields)
	}
	allowed := []string{"api_key"}
	required := [][]string{{"api_key"}}
	if scope.Resource.ScopeKind == "connection" {
		spec, ok := connectors.LookupConnection(scope.Provider)
		if !ok || len(spec.AuthKeys) == 0 || len(spec.RequiredAuthSets) == 0 {
			return nil, ErrInvalid
		}
		allowed, required = spec.AuthKeys, spec.RequiredAuthSets
	}
	if len(fields) == 0 || len(fields) > len(allowed) {
		return nil, ErrInvalid
	}
	size := 0
	for name, value := range fields {
		if !slices.Contains(allowed, name) || !utf8.ValidString(value) {
			return nil, ErrInvalid
		}
		size += len(name) + len(value)
		if size > encryption.MaxPlaintextSize {
			return nil, ErrInvalid
		}
	}
	valid := false
	for _, set := range required {
		present := len(set) > 0
		for _, name := range set {
			present = present && fields[name] != ""
		}
		valid = valid || present
	}
	if !valid {
		return nil, ErrInvalid
	}
	raw, err := json.Marshal(fields)
	if err != nil || len(raw) > encryption.MaxPlaintextSize {
		return nil, ErrInvalid
	}
	return raw, nil
}
