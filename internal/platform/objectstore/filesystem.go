package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	filesystemEnvelopeVersion uint32 = 1
	filesystemHeaderLimit            = 1 << 20
	filesystemObjectSuffix           = ".lvobj"
)

// FilesystemStoreConfig configures a durable local filesystem object store.
// Root must be an absolute, dedicated directory. A zero MaxObjectBytes uses
// MaxObjectBytes. StorageSecurityDomain is the store's isolation boundary.
type FilesystemStoreConfig struct {
	Root                  string
	StorageSecurityDomain string
	MaxObjectBytes        int64
	Now                   func() time.Time
}

// FilesystemStore is a crash-safe local implementation of ImmutableStore.
// Each object is one self-contained envelope file, so a committed key can
// never expose a separately published body and metadata pair.
type FilesystemStore struct {
	root           string
	securityDomain string
	maxObjectBytes int64
	now            func() time.Time
	locks          [64]sync.Mutex
}

// NewFilesystemStore constructs a filesystem store from explicit policy.
func NewFilesystemStore(config FilesystemStoreConfig) (*FilesystemStore, error) {
	if config.Root == "" || !filepath.IsAbs(config.Root) || !utf8.ValidString(config.Root) {
		return nil, fmt.Errorf("%w: filesystem root must be an absolute path", ErrInvalid)
	}
	root := filepath.Clean(config.Root)
	if root == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: filesystem root must be dedicated", ErrInvalid)
	}
	if err := validateSecurityDomain(config.StorageSecurityDomain); err != nil {
		return nil, err
	}
	max := config.MaxObjectBytes
	if max == 0 {
		max = MaxObjectBytes
	}
	if max < 1 || max > MaxObjectBytes {
		return nil, fmt.Errorf("%w: object byte limit", ErrInvalid)
	}
	if err := ensureFilesystemRoot(root); err != nil {
		return nil, err
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &FilesystemStore{root: root, securityDomain: config.StorageSecurityDomain, maxObjectBytes: max, now: now}, nil
}

func ensureFilesystemRoot(root string) error {
	info, err := os.Lstat(root)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(root)
		parentInfo, parentErr := os.Lstat(parent)
		if parentErr != nil {
			return fmt.Errorf("%w: inspect filesystem root parent: %v", ErrInvalid, parentErr)
		}
		if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
			return fmt.Errorf("%w: filesystem root parent must be a canonical directory", ErrInvalid)
		}
		canonicalParent, evalErr := filepath.EvalSymlinks(parent)
		if evalErr != nil || filepath.Clean(canonicalParent) != filepath.Clean(parent) {
			return fmt.Errorf("%w: filesystem root parent contains symlink components", ErrInvalid)
		}
		if err := os.Mkdir(root, 0o700); err != nil {
			return fmt.Errorf("%w: create filesystem root: %v", ErrInvalid, err)
		}
		created = true
		info, err = os.Lstat(root)
	}
	if err != nil {
		return fmt.Errorf("%w: inspect filesystem root: %v", ErrInvalid, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: filesystem root must be a directory and not a symlink", ErrInvalid)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(canonical) != root {
		return fmt.Errorf("%w: filesystem root contains symlink components", ErrInvalid)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("%w: secure filesystem root: %v", ErrInvalid, err)
	}
	if created {
		if err := syncDirectory(filepath.Dir(root)); err != nil {
			return fmt.Errorf("%w: sync filesystem root parent: %v", ErrInvalid, err)
		}
	}
	return nil
}

func validateFilesystemRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("%w: inspect filesystem root: %v", ErrInvalid, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: filesystem root was replaced or is not a directory", ErrInvalid)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(canonical) != filepath.Clean(root) {
		return fmt.Errorf("%w: filesystem root contains symlink components", ErrInvalid)
	}
	return nil
}

type filesystemEnvelope struct {
	Version uint32     `json:"version"`
	Info    ObjectInfo `json:"info"`
}

// PutImmutable streams the body to a private temporary file, verifies the
// caller's digest and size evidence, then publishes one complete envelope via
// an atomic no-overwrite hard link.
func (s *FilesystemStore) PutImmutable(ctx context.Context, key string, reader io.Reader, metadata ObjectMetadata) (ObjectInfo, error) {
	if s == nil {
		return ObjectInfo{}, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := contextErr(ctx); err != nil {
		return ObjectInfo{}, err
	}
	if err := validateFilesystemRoot(s.root); err != nil {
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
	target, parent, err := s.objectPath(key, true)
	if err != nil {
		return ObjectInfo{}, err
	}

	keyLock := s.keyLock(key)
	keyLock.Lock()
	defer keyLock.Unlock()
	// A pre-existing object is always verified before being used for replay.
	if _, err := os.Lstat(target); err == nil {
		return s.reconcileExisting(ctx, target, key, metadata)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ObjectInfo{}, mapFilesystemError(err, key)
	}

	bodyTemp, err := os.CreateTemp(parent, ".body-*")
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("%w: create temporary object: %v", ErrInvalid, err)
	}
	bodyPath := bodyTemp.Name()
	defer os.Remove(bodyPath)
	if err := bodyTemp.Chmod(0o600); err != nil {
		bodyTemp.Close()
		return ObjectInfo{}, fmt.Errorf("%w: secure temporary object: %v", ErrInvalid, err)
	}
	digest, size, copyErr := streamToFile(ctx, bodyTemp, reader, s.maxObjectBytes)
	if copyErr != nil {
		bodyTemp.Close()
		return ObjectInfo{}, copyErr
	}
	if err := bodyTemp.Sync(); err != nil {
		bodyTemp.Close()
		return ObjectInfo{}, fmt.Errorf("%w: sync temporary object: %v", ErrInvalid, err)
	}
	if err := bodyTemp.Close(); err != nil {
		return ObjectInfo{}, fmt.Errorf("%w: close temporary object: %v", ErrInvalid, err)
	}
	if err := contextErr(ctx); err != nil {
		return ObjectInfo{}, err
	}
	if metadata.Digest != digest {
		return ObjectInfo{}, fmt.Errorf("%w: digest got %s want %s", ErrInvalid, digest, metadata.Digest)
	}
	if metadata.SizeBytes != size {
		return ObjectInfo{}, fmt.Errorf("%w: size got %d want %d", ErrInvalid, size, metadata.SizeBytes)
	}
	createdAt := s.now().UTC()
	if createdAt.IsZero() {
		return ObjectInfo{}, fmt.Errorf("%w: creation time", ErrInvalid)
	}
	info := ObjectInfo{Key: key, StorageSecurityDomain: metadata.StorageSecurityDomain, Digest: digest, SizeBytes: size, ContentType: metadata.ContentType, MetadataDigest: metadata.MetadataDigest, CreatedAt: createdAt}

	envelopeTemp, err := os.CreateTemp(parent, ".envelope-*")
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("%w: create temporary envelope: %v", ErrInvalid, err)
	}
	envelopePath := envelopeTemp.Name()
	defer os.Remove(envelopePath)
	if err := envelopeTemp.Chmod(0o600); err != nil {
		envelopeTemp.Close()
		return ObjectInfo{}, fmt.Errorf("%w: secure temporary envelope: %v", ErrInvalid, err)
	}
	if err := writeEnvelope(ctx, envelopeTemp, bodyPath, filesystemEnvelope{Version: filesystemEnvelopeVersion, Info: info}); err != nil {
		envelopeTemp.Close()
		return ObjectInfo{}, err
	}
	if err := envelopeTemp.Sync(); err != nil {
		envelopeTemp.Close()
		return ObjectInfo{}, fmt.Errorf("%w: sync envelope: %v", ErrAmbiguous, err)
	}
	if err := envelopeTemp.Close(); err != nil {
		return ObjectInfo{}, fmt.Errorf("%w: close envelope: %v", ErrAmbiguous, err)
	}
	if err := os.Link(envelopePath, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return s.reconcileExisting(ctx, target, key, metadata)
		}
		return ObjectInfo{}, mapFilesystemError(err, key)
	}
	if err := syncDirectory(parent); err != nil {
		return info, fmt.Errorf("%w: object %q committed: %v", ErrAmbiguous, key, err)
	}
	return info, nil
}

func (s *FilesystemStore) reconcileExisting(ctx context.Context, path, key string, metadata ObjectMetadata) (ObjectInfo, error) {
	info, err := s.readAndVerify(ctx, path, key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if info.StorageSecurityDomain != metadata.StorageSecurityDomain || info.Digest != metadata.Digest || info.SizeBytes != metadata.SizeBytes || info.ContentType != metadata.ContentType || info.MetadataDigest != metadata.MetadataDigest {
		return ObjectInfo{}, fmt.Errorf("%w: key %q already contains different bytes or metadata", ErrConflict, key)
	}
	return info, nil
}

// Open verifies the complete envelope and body before returning a reader.
func (s *FilesystemStore) Open(ctx context.Context, key string) (Object, error) {
	if s == nil {
		return Object{}, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := contextErr(ctx); err != nil {
		return Object{}, err
	}
	if err := validateFilesystemRoot(s.root); err != nil {
		return Object{}, err
	}
	if err := validateKey(key); err != nil {
		return Object{}, err
	}
	path, _, err := s.objectPath(key, false)
	if err != nil {
		return Object{}, err
	}
	keyLock := s.keyLock(key)
	keyLock.Lock()
	defer keyLock.Unlock()
	info, offset, file, err := s.openAndVerify(ctx, path, key)
	if err != nil {
		return Object{}, err
	}
	if s.securityDomain != "" && info.StorageSecurityDomain != s.securityDomain {
		file.Close()
		return Object{}, fmt.Errorf("%w: key %q", ErrDomainMismatch, key)
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		file.Close()
		return Object{}, fmt.Errorf("%w: key %q body seek: %v", ErrCorrupt, key, err)
	}
	return Object{Body: &filesystemBody{File: file, remaining: info.SizeBytes}, Info: info}, nil
}

type filesystemBody struct {
	*os.File
	remaining int64
}

func (b *filesystemBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.File.Read(p)
	b.remaining -= int64(n)
	if err == nil && b.remaining == 0 {
		return n, nil
	}
	return n, err
}

// List returns bounded, lexicographically ordered keys using path-boundary
// prefix matching and a last-key cursor.
func (s *FilesystemStore) List(ctx context.Context, prefix, cursor string, limit int) ([]ObjectInfo, string, error) {
	if s == nil {
		return nil, "", fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := contextErr(ctx); err != nil {
		return nil, "", err
	}
	if err := validateFilesystemRoot(s.root); err != nil {
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
	infos := make([]ObjectInfo, 0)
	err := filepath.WalkDir(s.root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := contextErr(ctx); err != nil {
			return err
		}
		if walkErr != nil {
			return mapFilesystemError(walkErr, path)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink entry %q", ErrCorrupt, path)
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(entry.Name(), filesystemObjectSuffix) {
			return nil
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return fmt.Errorf("%w: derive object key: %v", ErrCorrupt, err)
		}
		key := filepath.ToSlash(strings.TrimSuffix(rel, filesystemObjectSuffix))
		if err := validateKey(key); err != nil {
			return fmt.Errorf("%w: malformed object path %q", ErrCorrupt, path)
		}
		if !prefixMatch(key, prefix) || (cursor != "" && key <= cursor) {
			return nil
		}
		keyLock := s.keyLock(key)
		keyLock.Lock()
		info, err := s.readAndVerify(ctx, path, key)
		keyLock.Unlock()
		if err != nil {
			return err
		}
		if s.securityDomain != "" && info.StorageSecurityDomain != s.securityDomain {
			return nil
		}
		infos = append(infos, info)
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, "", err
		}
		return nil, "", err
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Key < infos[j].Key })
	hasMore := len(infos) > limit
	if hasMore {
		infos = infos[:limit]
	}
	next := ""
	if hasMore {
		next = infos[len(infos)-1].Key
	}
	if err := contextErr(ctx); err != nil {
		return nil, "", err
	}
	return infos, next, nil
}

// Delete removes an exact key. Publication and removal are directory-synced;
// a post-removal sync failure is reported as ambiguous because the key may be
// gone even though acknowledgement was lost.
func (s *FilesystemStore) Delete(ctx context.Context, key string) error {
	if s == nil {
		return fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateFilesystemRoot(s.root); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	path, parent, err := s.objectPath(key, false)
	if err != nil {
		return err
	}
	keyLock := s.keyLock(key)
	keyLock.Lock()
	defer keyLock.Unlock()
	info, err := s.readAndVerify(ctx, path, key)
	if err != nil {
		return err
	}
	if s.securityDomain != "" && info.StorageSecurityDomain != s.securityDomain {
		return fmt.Errorf("%w: key %q", ErrDomainMismatch, key)
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: key %q", ErrNotFound, key)
		}
		return mapFilesystemError(err, key)
	}
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("%w: key %q removed: %v", ErrAmbiguous, key, err)
	}
	return nil
}

func (s *FilesystemStore) validateMetadata(metadata ObjectMetadata) error {
	if err := validateSecurityDomain(metadata.StorageSecurityDomain); err != nil {
		return err
	}
	if s.securityDomain != "" && metadata.StorageSecurityDomain != s.securityDomain {
		return fmt.Errorf("%w: got %q want %q", ErrDomainMismatch, metadata.StorageSecurityDomain, s.securityDomain)
	}
	if !isSHA256Identity(metadata.Digest) || !isSHA256Identity(metadata.MetadataDigest) {
		return fmt.Errorf("%w: digest and metadata digest must be canonical sha256 identities", ErrInvalid)
	}
	if metadata.SizeBytes < 0 || metadata.SizeBytes > s.maxObjectBytes {
		return fmt.Errorf("%w: object size", ErrInvalid)
	}
	if len(metadata.ContentType) > MaxContentTypeBytes || !utf8.ValidString(metadata.ContentType) || hasControl(metadata.ContentType) {
		return fmt.Errorf("%w: content type", ErrInvalid)
	}
	return nil
}

func (s *FilesystemStore) keyLock(key string) *sync.Mutex {
	// A small fixed stripe set bounds memory while allowing independent keys
	// to stream and verify concurrently. Colliding keys are still serialized.
	var hash byte
	for i := 0; i < len(key); i++ {
		hash = hash*33 + key[i]
	}
	return &s.locks[int(hash)%len(s.locks)]
}

func (s *FilesystemStore) objectPath(key string, createDirs bool) (string, string, error) {
	parts := strings.Split(key, "/")
	dir := s.root
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		if err := ensurePathDirectory(dir, createDirs); err != nil {
			return "", "", err
		}
	}
	name := parts[len(parts)-1] + filesystemObjectSuffix
	path := filepath.Join(dir, name)
	if !pathWithin(s.root, path) {
		return "", "", fmt.Errorf("%w: key %q escapes root", ErrInvalidKey, key)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("%w: symlink object path %q", ErrCorrupt, key)
	}
	return path, dir, nil
}

func ensurePathDirectory(path string, create bool) error {
	info, err := os.Lstat(path)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return fmt.Errorf("%w: missing object directory", ErrNotFound)
		}
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: create object directory: %v", ErrInvalid, err)
		}
		created = err == nil
		info, err = os.Lstat(path)
	}
	if err != nil {
		return mapFilesystemError(err, path)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: object path component is not a directory", ErrCorrupt)
	}
	if create {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("%w: secure object directory: %v", ErrInvalid, err)
		}
		if created {
			if err := syncDirectory(filepath.Dir(path)); err != nil {
				return fmt.Errorf("%w: sync object directory parent: %v", ErrInvalid, err)
			}
		}
	}
	return nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func streamToFile(ctx context.Context, dst *os.File, src io.Reader, max int64) (string, int64, error) {
	hash := sha256.New()
	reader := io.TeeReader(src, hash)
	var size int64
	buf := make([]byte, 32*1024)
	zeroReads := 0
	for {
		if err := contextErr(ctx); err != nil {
			return "", 0, err
		}
		n, err := reader.Read(buf)
		if n > 0 {
			if size+int64(n) > max {
				return "", 0, fmt.Errorf("%w: object exceeds %d bytes", ErrInvalid, max)
			}
			written, writeErr := dst.Write(buf[:n])
			if writeErr != nil || written != n {
				if writeErr == nil {
					writeErr = io.ErrShortWrite
				}
				return "", 0, fmt.Errorf("%w: write temporary object: %v", ErrInvalid, writeErr)
			}
			size += int64(n)
			zeroReads = 0
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", 0, err
		}
		if n == 0 {
			zeroReads++
			if zeroReads >= 100 {
				return "", 0, io.ErrNoProgress
			}
		}
	}
	if err := contextErr(ctx); err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, nil
}

func writeEnvelope(ctx context.Context, dst *os.File, bodyPath string, envelope filesystemEnvelope) error {
	header, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("%w: marshal metadata: %v", ErrInvalid, err)
	}
	if len(header) == 0 || len(header) > filesystemHeaderLimit {
		return fmt.Errorf("%w: metadata envelope size", ErrInvalid)
	}
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(header)))
	if _, err := dst.Write(length[:]); err != nil {
		return fmt.Errorf("%w: write metadata envelope: %v", ErrInvalid, err)
	}
	if _, err := dst.Write(header); err != nil {
		return fmt.Errorf("%w: write metadata envelope: %v", ErrInvalid, err)
	}
	body, err := os.Open(bodyPath)
	if err != nil {
		return fmt.Errorf("%w: reopen temporary object: %v", ErrInvalid, err)
	}
	defer body.Close()
	buf := make([]byte, 32*1024)
	for {
		if err := contextErr(ctx); err != nil {
			return err
		}
		n, readErr := body.Read(buf)
		if n > 0 {
			written, writeErr := dst.Write(buf[:n])
			if writeErr != nil || written != n {
				if writeErr == nil {
					writeErr = io.ErrShortWrite
				}
				return fmt.Errorf("%w: write object envelope: %v", ErrInvalid, writeErr)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return fmt.Errorf("%w: read temporary object: %v", ErrInvalid, readErr)
		}
	}
	return contextErr(ctx)
}

func (s *FilesystemStore) openAndVerify(ctx context.Context, path, key string) (ObjectInfo, int64, *os.File, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ObjectInfo{}, 0, nil, fmt.Errorf("%w: key %q", ErrNotFound, key)
	}
	if err != nil {
		return ObjectInfo{}, 0, nil, mapFilesystemError(err, key)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ObjectInfo{}, 0, nil, fmt.Errorf("%w: key %q is not a regular envelope", ErrCorrupt, key)
	}
	file, err := os.Open(path)
	if err != nil {
		return ObjectInfo{}, 0, nil, mapFilesystemError(err, key)
	}
	stored, offset, verifyErr := s.verifyEnvelope(ctx, file, key)
	if verifyErr != nil {
		file.Close()
		return ObjectInfo{}, 0, nil, verifyErr
	}
	return stored, offset, file, nil
}

func (s *FilesystemStore) readAndVerify(ctx context.Context, path, key string) (ObjectInfo, error) {
	info, _, file, err := s.openAndVerify(ctx, path, key)
	if file != nil {
		file.Close()
	}
	return info, err
}

func (s *FilesystemStore) verifyEnvelope(ctx context.Context, file *os.File, key string) (ObjectInfo, int64, error) {
	var length [8]byte
	if _, err := io.ReadFull(file, length[:]); err != nil {
		return ObjectInfo{}, 0, fmt.Errorf("%w: key %q metadata header: %v", ErrCorrupt, key, err)
	}
	headerSize := binary.BigEndian.Uint64(length[:])
	if headerSize == 0 || headerSize > filesystemHeaderLimit {
		return ObjectInfo{}, 0, fmt.Errorf("%w: key %q metadata header size", ErrCorrupt, key)
	}
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(file, header); err != nil {
		return ObjectInfo{}, 0, fmt.Errorf("%w: key %q metadata header: %v", ErrCorrupt, key, err)
	}
	var envelope filesystemEnvelope
	if err := json.Unmarshal(header, &envelope); err != nil || envelope.Version != filesystemEnvelopeVersion {
		return ObjectInfo{}, 0, fmt.Errorf("%w: key %q metadata envelope", ErrCorrupt, key)
	}
	stored := envelope.Info
	if err := validateStoredInfo(stored, key, s.maxObjectBytes); err != nil {
		return ObjectInfo{}, 0, err
	}
	hash := sha256.New()
	var size int64
	buf := make([]byte, 32*1024)
	zeroReads := 0
	for {
		if err := contextErr(ctx); err != nil {
			return ObjectInfo{}, 0, err
		}
		n, err := file.Read(buf)
		if n > 0 {
			_, _ = hash.Write(buf[:n])
			size += int64(n)
			if size > s.maxObjectBytes {
				return ObjectInfo{}, 0, fmt.Errorf("%w: key %q exceeds object limit", ErrCorrupt, key)
			}
			zeroReads = 0
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return ObjectInfo{}, 0, fmt.Errorf("%w: key %q body read: %v", ErrCorrupt, key, err)
		}
		if n == 0 {
			zeroReads++
			if zeroReads >= 100 {
				return ObjectInfo{}, 0, fmt.Errorf("%w: key %q body made no progress", ErrCorrupt, key)
			}
		}
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if stored.Digest != digest || stored.SizeBytes != size {
		return ObjectInfo{}, 0, fmt.Errorf("%w: key %q digest or size evidence mismatch", ErrCorrupt, key)
	}
	if err := contextErr(ctx); err != nil {
		return ObjectInfo{}, 0, err
	}
	offset := int64(8 + headerSize)
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ObjectInfo{}, 0, fmt.Errorf("%w: key %q body offset: %v", ErrCorrupt, key, err)
	}
	return stored, offset, nil
}

func validateStoredInfo(info ObjectInfo, key string, max int64) error {
	if info.Key != key || validateKey(info.Key) != nil || !isSHA256Identity(info.Digest) || !isSHA256Identity(info.MetadataDigest) || info.SizeBytes < 0 || info.SizeBytes > max || !utf8.ValidString(info.ContentType) || len(info.ContentType) > MaxContentTypeBytes || hasControl(info.ContentType) || info.CreatedAt.IsZero() {
		return fmt.Errorf("%w: key %q metadata evidence", ErrCorrupt, key)
	}
	if err := validateSecurityDomain(info.StorageSecurityDomain); err != nil {
		return fmt.Errorf("%w: key %q security domain", ErrCorrupt, key)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func mapFilesystemError(err error, key string) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: key %q", ErrNotFound, key)
	}
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w: key %q permission: %v", ErrInvalid, key, err)
	}
	return err
}
