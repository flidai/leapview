// Package authoringlists converts stable, named YAML/JSON declaration lists
// into indexed Go DTO fields without changing the authored wire contract.
package authoringlists

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Collection identifies an authored definition list and its identity member.
// Wrapper is set only when the indexed value is nested under another member.
type Collection struct {
	Path     string
	Identity string
	Wrapper  string
}

// Rewrite converts all declared collections in a JSON object. When toLists is
// false, duplicate identities fail before a map can silently overwrite them.
func Rewrite(encoded []byte, collections []Collection, toLists bool) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	object, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("authoring spec must be an object")
	}
	ordered := append([]Collection(nil), collections...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := strings.Count(ordered[i].Path, "."), strings.Count(ordered[j].Path, ".")
		if toLists {
			return left < right
		}
		return left > right
	})
	for _, collection := range ordered {
		segments := strings.Split(collection.Path, ".")
		if err := rewriteAt(object, segments, collection, toLists); err != nil {
			return nil, err
		}
	}
	return json.Marshal(object)
}

func rewriteAt(current any, segments []string, collection Collection, toLists bool) error {
	if len(segments) == 0 {
		return nil
	}
	if segments[0] == "*" {
		switch values := current.(type) {
		case map[string]any:
			for _, value := range values {
				if err := rewriteAt(value, segments[1:], collection, toLists); err != nil {
					return err
				}
			}
		case []any:
			for _, value := range values {
				if err := rewriteAt(value, segments[1:], collection, toLists); err != nil {
					return err
				}
			}
		}
		return nil
	}
	object, ok := current.(map[string]any)
	if !ok {
		return nil
	}
	value, exists := object[segments[0]]
	if !exists || value == nil {
		return nil
	}
	if len(segments) > 1 {
		return rewriteAt(value, segments[1:], collection, toLists)
	}
	if toLists {
		values, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: indexed Go value must be an object", collection.Path)
		}
		names := make([]string, 0, len(values))
		for name := range values {
			names = append(names, name)
		}
		sort.Strings(names)
		list := make([]any, 0, len(names))
		for _, name := range names {
			item := values[name]
			var entry map[string]any
			if collection.Wrapper != "" {
				entry = map[string]any{collection.Identity: name, collection.Wrapper: item}
			} else {
				source, ok := item.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.%s: definition must be an object", collection.Path, name)
				}
				entry = make(map[string]any, len(source)+1)
				for key, value := range source {
					entry[key] = value
				}
				entry[collection.Identity] = name
			}
			list = append(list, entry)
		}
		object[segments[0]] = list
		return nil
	}
	list, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s: authored collection must be a list", collection.Path)
	}
	indexed := make(map[string]any, len(list))
	for index, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("%s[%d]: definition must be an object", collection.Path, index)
		}
		name, ok := entry[collection.Identity].(string)
		if !ok || name == "" {
			return fmt.Errorf("%s[%d]: %s is required", collection.Path, index, collection.Identity)
		}
		if _, exists := indexed[name]; exists {
			return fmt.Errorf("%s: duplicate %s %q", collection.Path, collection.Identity, name)
		}
		if collection.Wrapper != "" {
			wrapped, ok := entry[collection.Wrapper]
			if !ok {
				return fmt.Errorf("%s[%d]: %s is required", collection.Path, index, collection.Wrapper)
			}
			indexed[name] = wrapped
		} else {
			// Retain identity in the generated Go member. Its enclosing map key
			// remains the compiler lookup key, while generated union decoders
			// require the authored identity field to be present.
			indexed[name] = entry
		}
	}
	object[segments[0]] = indexed
	return nil
}
