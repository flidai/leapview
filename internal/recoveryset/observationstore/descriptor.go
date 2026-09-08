package observationstore

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/flidai/leapview/internal/recoveryset/observation"
	"github.com/flidai/leapview/pkg/strictjson"
)

func parseDescriptor(raw []byte) (Descriptor, error) {
	if len(raw) < 2 || len(raw) > maxDescriptorBytes {
		return Descriptor{}, fmt.Errorf("%w: descriptor bytes are out of bounds", ErrIntegrity)
	}
	var descriptor Descriptor
	if err := strictjson.DecodeWithOptions(raw, &descriptor, strictjson.Options{MaxBytes: maxDescriptorBytes, MaxDepth: 32, DuplicateKeys: strictjson.CaseFoldedKeys, AllowUnknownFields: false}); err != nil {
		return Descriptor{}, fmt.Errorf("%w: decode descriptor: %v", ErrIntegrity, err)
	}
	// Preserve the distinction between an explicit empty array and an omitted
	// field used by encoding/json's zero value.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Descriptor{}, fmt.Errorf("%w: decode descriptor fields", ErrIntegrity)
	}
	if value, ok := fields["source_protections"]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return Descriptor{}, fmt.Errorf("%w: descriptor source protections must be an explicit array", ErrIntegrity)
	}
	if descriptor.SourceProtections == nil {
		descriptor.SourceProtections = make([]observation.Protection, 0)
	}
	if err := descriptor.Validate(); err != nil {
		return Descriptor{}, err
	}
	canonical, err := descriptor.CanonicalJSON()
	if err != nil || !bytes.Equal(raw, canonical) {
		return Descriptor{}, fmt.Errorf("%w: descriptor bytes are not canonical", ErrIntegrity)
	}
	return descriptor, nil
}

func (s *Store) evidencePrefix() string {
	if s.prefix == "" {
		return "recovery-observations"
	}
	return s.prefix + "/recovery-observations"
}

func (s *Store) evidenceKey(kind, digestValue string) string {
	return s.evidencePrefix() + "/" + kind + "/sha256/" + rawDigest(digestValue)[:2] + "/" + rawDigest(digestValue)
}

func (s *Store) frontierKey(digestValue string) string {
	return s.evidencePrefix() + "/frontiers/sha256/" + rawDigest(digestValue)[:2] + "/" + rawDigest(digestValue)
}

func (s *Store) setIDKey(id string) string { return s.evidencePrefix() + "/frontiers/set-id/" + id }
