package filter

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

var ErrStaleOptionRequest = errors.New("filter option request is stale")

type OptionRequest struct {
	BindingKey        string `json:"bindingKey"`
	Search            string `json:"search"`
	Cursor            string `json:"cursor,omitempty"`
	Limit             int    `json:"limit"`
	ServingStateID    string `json:"servingStateID"`
	FilterRevision    uint64 `json:"filterRevision"`
	RequestGeneration uint64 `json:"requestGeneration"`
}

type OptionItem struct {
	Value     Value  `json:"value"`
	Label     string `json:"label"`
	Null      bool   `json:"null"`
	Count     *int64 `json:"count,omitempty"`
	Selected  bool   `json:"selected"`
	Available bool   `json:"available"`
}

// MarshalJSON keeps the wire contract closed over null options: a null
// option has a stable null identity and no fabricated typed value. The domain
// field remains a Value for canonicalization and selection code, while the
// browser signal exposes value as optional.
func (item OptionItem) MarshalJSON() ([]byte, error) {
	type optionItemJSON struct {
		Value     *Value `json:"value,omitempty"`
		Label     string `json:"label"`
		Null      bool   `json:"null"`
		Count     *int64 `json:"count,omitempty"`
		Selected  bool   `json:"selected"`
		Available bool   `json:"available"`
	}
	var value *Value
	if !item.Null {
		value = &item.Value
	}
	return json.Marshal(optionItemJSON{Value: value, Label: item.Label, Null: item.Null, Count: item.Count, Selected: item.Selected, Available: item.Available})
}

type OptionPage struct {
	BindingKey        string       `json:"bindingKey"`
	ServingStateID    string       `json:"servingStateID"`
	StreamGeneration  uint64       `json:"streamGeneration"`
	FilterRevision    uint64       `json:"filterRevision"`
	RequestGeneration uint64       `json:"requestGeneration"`
	Items             []OptionItem `json:"items"`
	Complete          bool         `json:"complete"`
	NextCursor        string       `json:"nextCursor,omitempty"`
	ConsumerIdentity  string       `json:"consumerIdentity"`
}

type OptionContext struct {
	ServingStateID   string
	PolicyIdentity   string
	State            State
	Binding          Binding
	Definition       Definition
	BindingKeysByRef map[BindingRef]string
}

type OptionQuery struct {
	Field        string
	Dataset      string
	ValueKind    ValueKind
	Dependencies map[string]Expression
	Search       string
	After        string
	Limit        int
	IncludeNull  bool
	ShowCounts   bool
}

type OptionResult struct {
	Items    []OptionItem
	Complete bool
	Next     string
}

// CanonicalizeStaticOptions validates, deduplicates and semantically orders a
// compiled static option list. Equal typed values must carry the same label;
// conflicting labels are an authoring error rather than an arbitrary map
// winner.
func CanonicalizeStaticOptions(options []Option, kind ValueKind) ([]Option, error) {
	seen := make(map[string]Option, len(options))
	for index, option := range options {
		canonical, err := canonicalValue(option.Value, kind)
		if err != nil {
			return nil, fmt.Errorf("option %d: %w", index, err)
		}
		keyBytes, _ := json.Marshal(canonical)
		key := string(keyBytes)
		if previous, ok := seen[key]; ok {
			if previous.Label != option.Label {
				return nil, fmt.Errorf("duplicate typed option %q has conflicting labels %q and %q", canonical.Value, previous.Label, option.Label)
			}
			continue
		}
		option.Value = canonical
		seen[key] = option
	}
	result := make([]Option, 0, len(seen))
	for _, option := range seen {
		result = append(result, option)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return compareValues(result[i].Value, result[j].Value) < 0
	})
	return result, nil
}

type OptionQueryFunc func(context.Context, OptionQuery) (OptionResult, error)

type OptionEngine struct {
	secret []byte
	query  OptionQueryFunc
	cache  *OptionCache
}

type OptionCache struct {
	mu         sync.Mutex
	maxEntries int
	values     map[string]OptionResult
	order      []string
}

func NewOptionCache(maxEntries int) *OptionCache {
	if maxEntries <= 0 {
		maxEntries = 2048
	}
	return &OptionCache{maxEntries: maxEntries, values: make(map[string]OptionResult, maxEntries)}
}

func NewOptionEngine(secret []byte, query OptionQueryFunc) *OptionEngine {
	return NewOptionEngineWithCache(secret, NewOptionCache(2048), query)
}

func NewOptionEngineWithCache(secret []byte, cache *OptionCache, query OptionQueryFunc) *OptionEngine {
	if cache == nil {
		cache = NewOptionCache(2048)
	}
	return &OptionEngine{
		secret: append([]byte(nil), secret...), query: query,
		cache: cache,
	}
}

func (engine *OptionEngine) Page(ctx context.Context, optionContext OptionContext, request OptionRequest) (OptionPage, error) {
	if request.BindingKey == "" || request.BindingKey != optionContext.Binding.Key {
		return OptionPage{}, fmt.Errorf("filter option binding key does not match compiled binding")
	}
	if request.ServingStateID != optionContext.ServingStateID || request.FilterRevision != optionContext.State.Revision {
		return OptionPage{}, ErrStaleOptionRequest
	}
	if optionContext.PolicyIdentity == "" {
		return OptionPage{}, fmt.Errorf("filter option policy identity is required")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = optionContext.Definition.Options.Limit
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	search := strings.ToLower(strings.TrimSpace(request.Search))
	dependencies := incomingDependencies(optionContext)
	contextKey := optionCacheContext(optionContext, dependencies, search, limit)
	after := ""
	if request.Cursor != "" {
		cursor, err := engine.decodeCursor(request.Cursor)
		if err != nil {
			return OptionPage{}, err
		}
		if cursor.Context != contextKey {
			return OptionPage{}, ErrStaleOptionRequest
		}
		after = cursor.After
	}
	query := OptionQuery{
		Field: optionContext.Definition.Field, Dataset: optionContext.Definition.Dataset,
		ValueKind: optionContext.Definition.ValueKind, Dependencies: dependencies,
		Search: search, After: after, Limit: limit, IncludeNull: optionContext.Definition.Options.IncludeNull,
	}
	cacheKey := optionCacheKey(contextKey, after)
	result, ok := engine.cached(cacheKey)
	if !ok {
		var err error
		result, err = engine.load(ctx, optionContext.Definition, query)
		if err != nil {
			return OptionPage{}, err
		}
		engine.store(cacheKey, result)
	}
	items, err := canonicalOptionItems(result.Items, optionContext.Definition.ValueKind)
	if err != nil {
		return OptionPage{}, err
	}
	// Canonical ordering is applied before the post-order page limit. This is
	// important for typed integers/decimals where lexical order is different
	// from semantic order, and for null which is always ordered last.
	if len(items) > limit {
		items = items[:limit]
	}
	items = retainSelectedValues(items, optionContext.State.AppliedControls[request.BindingKey].Expression)
	nextCursor := ""
	if result.Next != "" {
		nextCursor, err = engine.encodeCursor(optionCursor{Context: contextKey, After: result.Next})
		if err != nil {
			return OptionPage{}, err
		}
	}
	return OptionPage{
		BindingKey: request.BindingKey, ServingStateID: request.ServingStateID,
		FilterRevision: request.FilterRevision, RequestGeneration: request.RequestGeneration,
		Items: items, Complete: result.Complete, NextCursor: nextCursor, ConsumerIdentity: "option:" + request.BindingKey,
	}, nil
}

func incomingDependencies(optionContext OptionContext) map[string]Expression {
	result := map[string]Expression{}
	for _, reference := range optionContext.Binding.OptionDependencies {
		key := optionContext.BindingKeysByRef[reference]
		if key == "" || key == optionContext.Binding.Key {
			continue
		}
		applied, ok := optionContext.State.AppliedControls[key]
		if !ok {
			continue
		}
		expression := applied.ResolvedExpression
		if expression.Kind == "" {
			expression = applied.Expression
		}
		if expression.Kind == ExpressionUnfiltered {
			continue
		}
		result[key] = cloneExpression(expression)
	}
	return result
}

func (engine *OptionEngine) load(ctx context.Context, definition Definition, query OptionQuery) (OptionResult, error) {
	if definition.Options.Kind == OptionSourceStatic {
		items := make([]OptionItem, 0, len(definition.Options.Values))
		for _, option := range definition.Options.Values {
			label := option.Label
			if label == "" {
				label = fmt.Sprint(option.Value.Value)
			}
			if query.Search != "" && !strings.Contains(strings.ToLower(label), query.Search) {
				continue
			}
			items = append(items, OptionItem{Value: option.Value, Label: label, Available: true})
		}
		return OptionResult{Items: items, Complete: true}, nil
	}
	if definition.Options.Kind != OptionSourceDistinct {
		return OptionResult{Items: []OptionItem{}, Complete: true}, nil
	}
	if engine.query == nil {
		return OptionResult{}, fmt.Errorf("distinct filter option query service is required")
	}
	return engine.query(ctx, query)
}

func canonicalOptionItems(items []OptionItem, kind ValueKind) ([]OptionItem, error) {
	result := make([]OptionItem, 0, len(items))
	seen := map[string]struct{}{}
	sawNull := false
	for _, item := range items {
		if item.Null {
			if sawNull {
				continue
			}
			sawNull = true
			item.Available = true
			if item.Label == "" {
				item.Label = "(null)"
			}
			result = append(result, item)
			continue
		}
		value, err := canonicalValue(item.Value, kind)
		if err != nil {
			return nil, err
		}
		keyBytes, _ := json.Marshal(value)
		key := string(keyBytes)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		item.Value = value
		item.Available = true
		if item.Label == "" {
			item.Label = fmt.Sprint(value.Value)
		}
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Null != result[j].Null {
			return !result[i].Null
		}
		if result[i].Null {
			return result[i].Label < result[j].Label
		}
		return compareValues(result[i].Value, result[j].Value) < 0
	})
	return result, nil
}

func retainSelectedValues(items []OptionItem, expression Expression) []OptionItem {
	selected := map[string]Value{}
	values := expression.Values
	if expression.Value != nil {
		values = append(values, *expression.Value)
	}
	for _, value := range values {
		keyBytes, _ := json.Marshal(value)
		selected[string(keyBytes)] = value
	}
	for index := range items {
		if items[index].Null {
			continue
		}
		keyBytes, _ := json.Marshal(items[index].Value)
		key := string(keyBytes)
		if _, ok := selected[key]; ok {
			items[index].Selected = true
			delete(selected, key)
		}
	}
	keys := make([]string, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := selected[key]
		items = append(items, OptionItem{
			Value: value, Label: fmt.Sprint(value.Value), Selected: true, Available: false,
		})
	}
	return items
}

func optionCacheContext(optionContext OptionContext, dependencies map[string]Expression, search string, limit int) string {
	payload := struct {
		ServingState string
		Policy       string
		Binding      string
		Dependencies map[string]Expression
		Search       string
		Limit        int
	}{
		optionContext.ServingStateID, optionContext.PolicyIdentity, optionContext.Binding.Key,
		dependencies, search, limit,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func optionCacheKey(contextKey, after string) string {
	return contextKey + "\x00" + after
}

func (engine *OptionEngine) cached(key string) (OptionResult, bool) {
	return engine.cache.get(key)
}

func (engine *OptionEngine) store(key string, result OptionResult) {
	engine.cache.put(key, result)
}

func (cache *OptionCache) get(key string) (OptionResult, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	result, ok := cache.values[key]
	return cloneOptionResult(result), ok
}

func (cache *OptionCache) put(key string, result OptionResult) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if _, exists := cache.values[key]; !exists {
		cache.order = append(cache.order, key)
	}
	cache.values[key] = cloneOptionResult(result)
	for len(cache.order) > cache.maxEntries {
		oldest := cache.order[0]
		cache.order = cache.order[1:]
		delete(cache.values, oldest)
	}
}

func cloneOptionResult(result OptionResult) OptionResult {
	result.Items = append([]OptionItem(nil), result.Items...)
	return result
}

type optionCursor struct {
	Context string `json:"context"`
	After   string `json:"after"`
}

func (engine *OptionEngine) encodeCursor(cursor optionCursor) (string, error) {
	if len(engine.secret) < 32 {
		return "", fmt.Errorf("filter option cursor signing secret must be at least 32 bytes")
	}
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, engine.secret)
	_, _ = mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (engine *OptionEngine) decodeCursor(value string) (optionCursor, error) {
	if len(engine.secret) < 32 {
		return optionCursor{}, fmt.Errorf("filter option cursor signing secret must be at least 32 bytes")
	}
	body, signature, ok := strings.Cut(value, ".")
	if !ok {
		return optionCursor{}, fmt.Errorf("invalid filter option cursor")
	}
	provided, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return optionCursor{}, fmt.Errorf("invalid filter option cursor")
	}
	mac := hmac.New(sha256.New, engine.secret)
	_, _ = mac.Write([]byte(body))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return optionCursor{}, fmt.Errorf("invalid filter option cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return optionCursor{}, fmt.Errorf("invalid filter option cursor")
	}
	var cursor optionCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Context == "" {
		return optionCursor{}, fmt.Errorf("invalid filter option cursor")
	}
	return cursor, nil
}
