// Package cache contains dependency-addressed result-cache contracts.  It is
// deliberately independent of result storage: Arrow bytes belong in L1/L2/L3
// tiers while this package owns the immutable identity used to coordinate
// those tiers.
package cache

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

const (
	// QueryDigestVersion is bumped whenever query-equivalence serialization
	// changes.  Typed values are intentionally part of this wire contract.
	QueryDigestVersion = 1
	// CacheKeyVersion is the composite identity version persisted by the cache
	// metadata repository.
	CacheKeyVersion = resultidentity.CacheKeyFormatVersion
	// SpatialTileFormatVersion is part of query equivalence because changing
	// MVT promotion/encoding must never reuse bytes produced by an older
	// renderer contract.
	SpatialTileFormatVersion = 5
	queryDigestDomain        = "flid.resultidentity.query.v1"
	cacheKeyDomain           = "flid.resultidentity.cache-key.v2"
)

var (
	ErrInvalidKey   = errors.New("invalid result cache key")
	ErrInvalidQuery = errors.New("invalid canonical query")
)

// KeyInput is the complete equivalence evidence for one result.  Partition,
// dependency and effective policy are separate values so callers cannot
// accidentally treat a content digest as an authorization boundary.
type KeyInput struct {
	Partition                  resultidentity.Partition
	Dependency                 resultidentity.Dependency
	EffectivePolicyFingerprint string
	CanonicalQueryDigest       string
}

// Key is an immutable, content-addressed cache identity.
type Key struct {
	partition         resultidentity.Partition
	dependencyDigest  string
	policyFingerprint string
	queryDigest       string
	canonical         []byte
	digest            string
}

// NewKey validates and canonicalizes the composite cache identity.
func NewKey(input KeyInput) (Key, error) {
	if input.Dependency.Version() == 0 || input.Dependency.Digest() == "" {
		return Key{}, fmt.Errorf("%w: dependency evidence is unavailable", ErrInvalidKey)
	}
	return NewKeyFromDigests(input.Partition, input.Dependency.Digest(), input.EffectivePolicyFingerprint, input.CanonicalQueryDigest)
}

// NewKeyFromDigests reconstructs the canonical composite key from durable
// identity digests.  Durable repositories use this to prove that their
// denormalized manifest columns represent exactly the same key produced by
// NewKey; there is deliberately only one wire serializer.
func NewKeyFromDigests(partition resultidentity.Partition, dependencyDigest, policyFingerprint, canonicalQueryDigest string) (Key, error) {
	if partition.Version() == 0 {
		return Key{}, fmt.Errorf("%w: partition is unavailable", ErrInvalidKey)
	}
	if err := validateDigest("dependency", dependencyDigest); err != nil {
		return Key{}, err
	}
	if err := validateDigest("effective policy fingerprint", policyFingerprint); err != nil {
		return Key{}, err
	}
	if err := validateDigest("canonical query digest", canonicalQueryDigest); err != nil {
		return Key{}, err
	}
	var partitionJSON json.RawMessage = partition.Canonical()
	wire := struct {
		Version              int             `json:"version"`
		Partition            json.RawMessage `json:"partition"`
		DependencyDigest     string          `json:"dependencyDigest"`
		PolicyFingerprint    string          `json:"policyFingerprint"`
		CanonicalQueryDigest string          `json:"canonicalQueryDigest"`
	}{CacheKeyVersion, partitionJSON, dependencyDigest, policyFingerprint, canonicalQueryDigest}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return Key{}, fmt.Errorf("%w: serialize: %v", ErrInvalidKey, err)
	}
	digest := domainSeparatedSHA256(cacheKeyDomain, canonical)
	return Key{partition: partition, dependencyDigest: dependencyDigest, policyFingerprint: policyFingerprint, queryDigest: canonicalQueryDigest, canonical: append([]byte(nil), canonical...), digest: digest}, nil
}

func validateDigest(name, value string) error {
	if err := platformdigest.ValidateSHA256Identity(value); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidKey, name, err)
	}
	return nil
}

func (k Key) Version() int {
	if len(k.canonical) == 0 {
		return 0
	}
	return CacheKeyVersion
}
func (k Key) Partition() resultidentity.Partition { return k.partition }
func (k Key) DependencyDigest() string            { return k.dependencyDigest }
func (k Key) PolicyFingerprint() string           { return k.policyFingerprint }
func (k Key) CanonicalQueryDigest() string        { return k.queryDigest }
func (k Key) Canonical() []byte                   { return append([]byte(nil), k.canonical...) }
func (k Key) Digest() string                      { return k.digest }

// PartitionIdentity returns the exact canonical partition bytes suitable for
// an L1 scope identity. It intentionally excludes serving-generation state.
func PartitionIdentity(partition resultidentity.Partition) string {
	return string(partition.Canonical())
}

// CanonicalQueryDigest returns the digest of all result-affecting query
// semantics. Request, principal, correlation and other audit identities are
// intentionally excluded; the fully resolved policy is supplied separately.
func CanonicalQueryDigest(query dataquery.Query) (string, error) {
	wire := queryWire{
		Version: QueryDigestVersion, Surface: query.Surface, Operation: query.Operation,
		ModelID: query.ModelID, Kind: query.Kind, Target: query.Target,
		Fields: query.Fields, Metrics: query.Metrics, AuthorizationFields: query.AuthorizationFields,
		Value: query.Value, Time: query.Time, Sort: query.Sort,
		ColumnMasks: query.ColumnMasks, Offset: query.Offset, Limit: query.Limit,
		BinCount: query.BinCount, Histogram: query.Histogram, Distribution: query.Distribution,
		IncludeTotal: query.IncludeTotal, SpatialTile: query.SpatialTile,
		SpatialTileBudget: query.SpatialTileBudget, SpatialMetadata: query.SpatialMetadata,
	}
	if query.SpatialTile != nil || query.SpatialTileBudget != nil {
		wire.SpatialTileFormatVersion = SpatialTileFormatVersion
	}
	filters, err := digestFilters(query.Filters)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidQuery, err)
	}
	wire.Filters = filters
	canonical, err := json.Marshal(wire)
	if err != nil {
		return "", fmt.Errorf("%w: serialize: %v", ErrInvalidQuery, err)
	}
	return domainSeparatedSHA256(queryDigestDomain, canonical), nil
}

// domainSeparatedSHA256 streams the preimage into the hash. Besides avoiding a
// second copy of potentially large canonical query bytes, this keeps capacity
// arithmetic out of the allocation path.
func domainSeparatedSHA256(domain string, canonical []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(canonical)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

type queryWire struct {
	Version                  int                            `json:"version"`
	Surface                  string                         `json:"surface"`
	Operation                string                         `json:"operation"`
	ModelID                  string                         `json:"modelId"`
	Kind                     dataquery.Kind                 `json:"kind"`
	Target                   string                         `json:"target"`
	Fields                   []dataquery.Field              `json:"fields,omitempty"`
	Metrics                  []dataquery.Field              `json:"metrics,omitempty"`
	AuthorizationFields      []dataquery.Field              `json:"authorizationFields,omitempty"`
	Value                    dataquery.Field                `json:"value,omitempty"`
	Time                     dataquery.Time                 `json:"time,omitempty"`
	Filters                  []digestFilter                 `json:"filters,omitempty"`
	Sort                     []dataquery.Sort               `json:"sort,omitempty"`
	ColumnMasks              []dataquery.ColumnMask         `json:"columnMasks,omitempty"`
	Offset                   int                            `json:"offset"`
	Limit                    int                            `json:"limit"`
	BinCount                 int                            `json:"binCount"`
	Histogram                *dataquery.HistogramOptions    `json:"histogram,omitempty"`
	Distribution             *dataquery.DistributionOptions `json:"distribution,omitempty"`
	IncludeTotal             bool                           `json:"includeTotal"`
	SpatialTile              *dataquery.SpatialTile         `json:"spatialTile,omitempty"`
	SpatialTileBudget        *dataquery.SpatialTileBudget   `json:"spatialTileBudget,omitempty"`
	SpatialMetadata          *dataquery.SpatialMetadata     `json:"spatialMetadata,omitempty"`
	SpatialTileFormatVersion int                            `json:"spatialTileFormatVersion,omitempty"`
}

type digestFilter struct {
	Field    string                   `json:"field"`
	Dataset  string                   `json:"dataset,omitempty"`
	Operator string                   `json:"operator"`
	Values   []any                    `json:"values,omitempty"`
	Groups   []digestFilterGroup      `json:"groups,omitempty"`
	Spatial  *dataquery.SpatialFilter `json:"spatial,omitempty"`
}
type digestFilterGroup struct {
	Filters []digestFilter `json:"filters,omitempty"`
}

func digestFilters(filters []dataquery.Filter) ([]digestFilter, error) {
	out := make([]digestFilter, len(filters))
	for i, filter := range filters {
		values := make([]any, len(filter.Values))
		for j, value := range filter.Values {
			converted, err := typedReflect(value)
			if err != nil {
				return nil, fmt.Errorf("filter %d value %d: %w", i, j, err)
			}
			values[j] = converted
		}
		groups := make([]digestFilterGroup, len(filter.Groups))
		for j, group := range filter.Groups {
			nested, err := digestFilters(group.Filters)
			if err != nil {
				return nil, err
			}
			groups[j] = digestFilterGroup{Filters: nested}
		}
		out[i] = digestFilter{Field: filter.Field, Dataset: filter.Dataset, Operator: filter.Operator, Values: values, Groups: groups, Spatial: filter.Spatial}
	}
	return out, nil
}

// TypedValueCanonical is provided for callers that need to prove the typed
// identity of a bound value independently of a complete Query.
func TypedValueCanonical(value any) ([]byte, error) {
	converted, err := typedReflect(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(converted)
}

func typedReflect(value any) (any, error) {
	if value == nil {
		return map[string]any{"type": "null"}, nil
	}
	if number, ok := value.(json.Number); ok {
		if _, err := strconv.ParseFloat(string(number), 64); err != nil {
			return nil, fmt.Errorf("invalid json.Number %q", number)
		}
		return map[string]any{"type": "json.Number", "value": string(number)}, nil
	}
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			if rv.Kind() == reflect.Pointer {
				return map[string]any{"type": "pointer:" + rv.Type().String(), "value": map[string]any{"type": "null"}}, nil
			}
			return map[string]any{"type": "null"}, nil
		}
		if rv.Kind() == reflect.Pointer {
			value, err := typedReflect(rv.Elem().Interface())
			if err != nil {
				return nil, err
			}
			return map[string]any{"type": "pointer:" + rv.Type().String(), "value": value}, nil
		}
		rv = rv.Elem()
		value = rv.Interface()
	}
	switch rv.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "bool", "value": rv.Bool()}, nil
	case reflect.String:
		return map[string]any{"type": "string", "value": rv.String()}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": rv.Type().String(), "value": strconv.FormatInt(rv.Int(), 10)}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": rv.Type().String(), "value": strconv.FormatUint(rv.Uint(), 10)}, nil
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("non-finite float %v", f)
		}
		return map[string]any{"type": rv.Type().String(), "value": strconv.FormatFloat(f, 'g', -1, rv.Type().Bits())}, nil
	case reflect.Slice:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": rv.Type().String(), "value": base64.StdEncoding.EncodeToString(rv.Bytes())}, nil
		}
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			v, err := typedReflect(rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case reflect.Array:
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			v, err := typedReflect(rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("map key type %s is not supported", rv.Type().Key())
		}
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		out := make(map[string]any, rv.Len())
		for _, key := range keys {
			v, err := typedReflect(rv.MapIndex(key).Interface())
			if err != nil {
				return nil, err
			}
			out[key.String()] = v
		}
		return out, nil
	case reflect.Struct:
		if t, ok := value.(time.Time); ok {
			return map[string]any{"type": "time.Time", "value": t.UTC().Format(time.RFC3339Nano)}, nil
		}
	}
	return nil, fmt.Errorf("unsupported query value type %T", value)
}
