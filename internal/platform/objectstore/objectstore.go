// Package objectstore defines the provider-neutral immutable object boundary
// used by native delivery. Objects are content-addressed and create-only:
// once a key is committed, it can never be overwritten by different bytes or
// metadata.
package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxObjectBytes keeps untrusted uploads bounded. Durable adapters may use
	// a tighter limit.
	MaxObjectBytes int64 = 1 << 30
	// MaxContentTypeBytes bounds provider media-type metadata.
	MaxContentTypeBytes = 255
	MaxKeyBytes         = 4096
	MaxPrefixBytes      = 4096
	MaxListLimit        = 1000
)

var (
	ErrInvalid        = errors.New("invalid object-store input")
	ErrInvalidKey     = errors.New("invalid object-store key")
	ErrInvalidPrefix  = errors.New("invalid object-store prefix")
	ErrNotFound       = errors.New("object not found")
	ErrConflict       = errors.New("object immutable conflict")
	ErrAmbiguous      = errors.New("object commit acknowledgement is ambiguous")
	ErrDomainMismatch = errors.New("object storage security domain mismatch")
	ErrCorrupt        = errors.New("object content or metadata is corrupt")
)

// ObjectMetadata is the expected identity and bounded provider metadata for a
// put. Digest and SizeBytes are caller-precommitted evidence and are checked
// against the bytes read from the supplied reader. MetadataDigest identifies
// the caller's external canonical metadata envelope; the store does not
// recompute that envelope and only validates its canonical sha256 form.
type ObjectMetadata struct {
	StorageSecurityDomain string
	Digest                string
	SizeBytes             int64
	ContentType           string
	MetadataDigest        string
}

// ObjectInfo is the immutable identity and bounded provider metadata returned
// by writes, reads, and listings. Digest and MetadataDigest use canonical
// "sha256:<lowercase-hex>" identities.
type ObjectInfo struct {
	Key                   string
	StorageSecurityDomain string
	Digest                string
	SizeBytes             int64
	ContentType           string
	MetadataDigest        string
	CreatedAt             time.Time
}

// Object is an exact-key immutable object. Callers must close Body after
// reading it.
type Object struct {
	Body io.ReadCloser
	Info ObjectInfo
}

// ImmutableStore is the provider-neutral immutable object contract.
type ImmutableStore interface {
	PutImmutable(context.Context, string, io.Reader, ObjectMetadata) (ObjectInfo, error)
	Open(context.Context, string) (Object, error)
	List(context.Context, string, string, int) ([]ObjectInfo, string, error)
	Delete(context.Context, string) error
}

// MemoryStore is a concurrency-safe reference implementation for tests and
// local development. It copies all bytes before retaining or returning them.
// Production deployments should inject a durable provider adapter instead.
type MemoryStore struct {
	mu             sync.RWMutex
	objects        map[string]memoryObject
	securityDomain string
	maxObjectBytes int64
	now            func() time.Time
	loseNextAck    bool
}

type memoryObject struct {
	info ObjectInfo
	body []byte
}

// MemoryStoreConfig configures the reference store. Zero limits use package
// defaults. StorageSecurityDomain, when set, is an isolation boundary: puts
// for another domain are rejected and listings never expose them.
type MemoryStoreConfig struct {
	StorageSecurityDomain string
	MaxObjectBytes        int64
	Now                   func() time.Time
}

// NewMemoryStore constructs a configured in-memory reference store.
func NewMemoryStore(config MemoryStoreConfig) (*MemoryStore, error) {
	if config.StorageSecurityDomain != "" {
		if err := validateSecurityDomain(config.StorageSecurityDomain); err != nil {
			return nil, err
		}
	}
	maxObjectBytes := config.MaxObjectBytes
	if maxObjectBytes == 0 {
		maxObjectBytes = MaxObjectBytes
	}
	if maxObjectBytes < 1 || maxObjectBytes > MaxObjectBytes {
		return nil, fmt.Errorf("%w: object byte limit", ErrInvalid)
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemoryStore{
		objects:        make(map[string]memoryObject),
		securityDomain: config.StorageSecurityDomain,
		maxObjectBytes: maxObjectBytes,
		now:            now,
	}, nil
}

// SetLoseNextCommitAcknowledgement asks the reference store to commit the
// next new object but return ErrAmbiguous, modelling a lost provider response.
// The flag is consumed atomically by the next successful create.
func (s *MemoryStore) SetLoseNextCommitAcknowledgement(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.loseNextAck = enabled
	s.mu.Unlock()
}

// SimulateLostCommitAcknowledgement enables one-shot acknowledgement loss.
func (s *MemoryStore) SimulateLostCommitAcknowledgement() {
	s.SetLoseNextCommitAcknowledgement(true)
}

// PutImmutable creates key exactly once. Exact replays return the original
// ObjectInfo; differing bytes or metadata return ErrConflict.
func (s *MemoryStore) PutImmutable(ctx context.Context, key string, reader io.Reader, metadata ObjectMetadata) (ObjectInfo, error) {
	if s == nil {
		return ObjectInfo{}, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := contextErr(ctx); err != nil {
		return ObjectInfo{}, err
	}
	if err := validateKey(key); err != nil {
		return ObjectInfo{}, err
	}
	if reader == nil {
		return ObjectInfo{}, fmt.Errorf("%w: nil object reader", ErrInvalid)
	}
	if err := s.validateMetadata(metadata); err != nil {
		return ObjectInfo{}, err
	}
	body, err := readBody(ctx, reader, s.objectLimit())
	if err != nil {
		return ObjectInfo{}, err
	}
	digest := sha256Identity(body)
	if metadata.Digest != digest {
		return ObjectInfo{}, fmt.Errorf("%w: digest got %s want %s", ErrInvalid, digest, metadata.Digest)
	}
	if int64(len(body)) != metadata.SizeBytes {
		return ObjectInfo{}, fmt.Errorf("%w: size got %d want %d", ErrInvalid, len(body), metadata.SizeBytes)
	}
	createdAt := s.currentTime()
	if createdAt.IsZero() {
		return ObjectInfo{}, fmt.Errorf("%w: creation time", ErrInvalid)
	}
	info := ObjectInfo{
		Key:                   key,
		StorageSecurityDomain: metadata.StorageSecurityDomain,
		Digest:                digest,
		SizeBytes:             int64(len(body)),
		ContentType:           metadata.ContentType,
		MetadataDigest:        metadata.MetadataDigest,
		CreatedAt:             createdAt,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.objects == nil {
		s.objects = make(map[string]memoryObject)
	}
	if existing, ok := s.objects[key]; ok {
		if existing.info.Digest == info.Digest && existing.info.SizeBytes == info.SizeBytes && existing.info.StorageSecurityDomain == info.StorageSecurityDomain && existing.info.ContentType == info.ContentType && existing.info.MetadataDigest == info.MetadataDigest {
			return existing.info, nil
		}
		return ObjectInfo{}, fmt.Errorf("%w: key %q already contains different bytes or metadata", ErrConflict, key)
	}
	s.objects[key] = memoryObject{info: info, body: append([]byte(nil), body...)}
	if s.loseNextAck {
		s.loseNextAck = false
		return info, fmt.Errorf("%w: key %q committed", ErrAmbiguous, key)
	}
	return info, nil
}

// Open returns an independent reader over exact immutable bytes and verifies
// the stored digest and size evidence before exposing them.
func (s *MemoryStore) Open(ctx context.Context, key string) (Object, error) {
	if s == nil {
		return Object{}, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := contextErr(ctx); err != nil {
		return Object{}, err
	}
	if err := validateKey(key); err != nil {
		return Object{}, err
	}
	s.mu.RLock()
	stored, ok := s.objects[key]
	s.mu.RUnlock()
	if !ok {
		return Object{}, fmt.Errorf("%w: key %q", ErrNotFound, key)
	}
	if err := verifyObject(stored); err != nil {
		return Object{}, err
	}
	if s.securityDomain != "" && stored.info.StorageSecurityDomain != s.securityDomain {
		return Object{}, fmt.Errorf("%w: key %q", ErrDomainMismatch, key)
	}
	if err := contextErr(ctx); err != nil {
		return Object{}, err
	}
	return Object{Body: io.NopCloser(bytes.NewReader(append([]byte(nil), stored.body...))), Info: stored.info}, nil
}

// List returns at most limit objects whose keys begin with prefix as a path
// boundary, sorted lexicographically. Cursor is the last key from the prior
// page; an empty cursor starts at the beginning. The returned cursor is empty
// on the final page.
func (s *MemoryStore) List(ctx context.Context, prefix, cursor string, limit int) ([]ObjectInfo, string, error) {
	if s == nil {
		return nil, "", fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := contextErr(ctx); err != nil {
		return nil, "", err
	}
	if err := validatePrefix(prefix); err != nil {
		return nil, "", err
	}
	if cursor != "" {
		if err := validateKey(cursor); err != nil {
			return nil, "", fmt.Errorf("%w: cursor: %v", ErrInvalid, err)
		}
	}
	if limit < 1 || limit > MaxListLimit {
		return nil, "", fmt.Errorf("%w: list limit must be between 1 and %d", ErrInvalid, MaxListLimit)
	}
	s.mu.RLock()
	keys := make([]string, 0, len(s.objects))
	for key, stored := range s.objects {
		if !prefixMatch(key, prefix) || (cursor != "" && key <= cursor) {
			continue
		}
		if s.securityDomain != "" && stored.info.StorageSecurityDomain != s.securityDomain {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hasMore := len(keys) > limit
	if hasMore {
		keys = keys[:limit]
	}
	infos := make([]ObjectInfo, 0, len(keys))
	for _, key := range keys {
		infos = append(infos, s.objects[key].info)
	}
	s.mu.RUnlock()
	if err := contextErr(ctx); err != nil {
		return nil, "", err
	}
	next := ""
	if hasMore {
		next = infos[len(infos)-1].Key
	}
	return infos, next, nil
}

// Delete removes one immutable object. Deleting a missing key is reported so
// callers cannot mistake a typo for successful garbage collection.
func (s *MemoryStore) Delete(ctx context.Context, key string) error {
	if s == nil {
		return fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objects[key]; !ok {
		return fmt.Errorf("%w: key %q", ErrNotFound, key)
	}
	delete(s.objects, key)
	return nil
}

func (s *MemoryStore) validateMetadata(metadata ObjectMetadata) error {
	if err := validateSecurityDomain(metadata.StorageSecurityDomain); err != nil {
		return err
	}
	if s.securityDomain != "" && metadata.StorageSecurityDomain != s.securityDomain {
		return fmt.Errorf("%w: got %q want %q", ErrDomainMismatch, metadata.StorageSecurityDomain, s.securityDomain)
	}
	if !isSHA256Identity(metadata.Digest) || !isSHA256Identity(metadata.MetadataDigest) {
		return fmt.Errorf("%w: digest and metadata digest must be canonical sha256 identities", ErrInvalid)
	}
	if metadata.SizeBytes < 0 || metadata.SizeBytes > s.objectLimit() {
		return fmt.Errorf("%w: object size", ErrInvalid)
	}
	if len(metadata.ContentType) > MaxContentTypeBytes || !utf8.ValidString(metadata.ContentType) || hasControl(metadata.ContentType) {
		return fmt.Errorf("%w: content type", ErrInvalid)
	}
	return nil
}

func validateKey(key string) error {
	if key == "" || len(key) > MaxKeyBytes || strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") || strings.HasPrefix(key, "\\") || strings.Contains(key, "\\") || !utf8.ValidString(key) {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." || (len(segment) >= 2 && segment[1] == ':') || hasControl(segment) {
			return fmt.Errorf("%w: %q", ErrInvalidKey, key)
		}
	}
	return nil
}

func validatePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if len(prefix) > MaxPrefixBytes || strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("%w: %q", ErrInvalidPrefix, prefix)
	}
	if err := validateKey(prefix); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPrefix, err)
	}
	return nil
}

func prefixMatch(key, prefix string) bool {
	return prefix == "" || key == prefix || strings.HasPrefix(key, prefix+"/")
}

func validateSecurityDomain(domain string) error {
	if domain == "" || len(domain) > 512 || strings.TrimSpace(domain) != domain || !utf8.ValidString(domain) || hasControl(domain) {
		return fmt.Errorf("%w: storage security domain", ErrInvalid)
	}
	return nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func readBody(ctx context.Context, reader io.Reader, max int64) ([]byte, error) {
	if max < 1 {
		return nil, fmt.Errorf("%w: object byte limit", ErrInvalid)
	}
	var body []byte
	chunk := make([]byte, 32*1024)
	zeroReads := 0
	for {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		read, err := reader.Read(chunk)
		if read > 0 {
			if int64(len(body)+read) > max {
				return nil, fmt.Errorf("%w: object exceeds %d bytes", ErrInvalid, max)
			}
			body = append(body, chunk[:read]...)
			zeroReads = 0
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if read == 0 {
			zeroReads++
			if zeroReads >= 100 {
				return nil, io.ErrNoProgress
			}
		}
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	return body, nil
}

func verifyObject(stored memoryObject) error {
	if stored.info.Digest != sha256Identity(stored.body) || stored.info.SizeBytes != int64(len(stored.body)) {
		return fmt.Errorf("%w: key %q digest or size evidence mismatch", ErrCorrupt, stored.info.Key)
	}
	if err := validateSecurityDomain(stored.info.StorageSecurityDomain); err != nil || !isSHA256Identity(stored.info.MetadataDigest) {
		return fmt.Errorf("%w: key %q metadata evidence mismatch", ErrCorrupt, stored.info.Key)
	}
	if len(stored.info.ContentType) > MaxContentTypeBytes || hasControl(stored.info.ContentType) || stored.info.CreatedAt.IsZero() {
		return fmt.Errorf("%w: key %q provider metadata mismatch", ErrCorrupt, stored.info.Key)
	}
	return nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalid)
	}
	return ctx.Err()
}

func sha256Identity(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func isSHA256Identity(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func (s *MemoryStore) currentTime() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func (s *MemoryStore) objectLimit() int64 {
	if s.maxObjectBytes <= 0 {
		return MaxObjectBytes
	}
	return s.maxObjectBytes
}
