// Package filesystem implements private, content-addressed managed-data storage.
package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/storage"
)

const (
	privateDir   = 0o700
	readOnlyDir  = 0o500
	privateFile  = 0o600
	readOnlyFile = 0o400
)

type Store struct {
	root      string
	blobs     string
	staging   string
	revisions string
}

func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: filesystem root is required", storage.ErrInvalid)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve filesystem root", storage.ErrInvalid)
	}
	store := &Store{
		root:      absRoot,
		blobs:     filepath.Join(absRoot, "blobs", "sha256"),
		staging:   filepath.Join(absRoot, "staging"),
		revisions: filepath.Join(absRoot, "revisions"),
	}
	for _, directory := range []string{store.root, filepath.Join(store.root, "blobs"), store.blobs, store.staging, store.revisions} {
		if err := mkdirPrivate(directory); err != nil {
			return nil, fmt.Errorf("initialize filesystem blob store: %w", err)
		}
	}
	return store, nil
}

func (s *Store) Put(ctx context.Context, expected storage.Blob, content io.Reader) (storage.Blob, error) {
	if err := storage.ValidateBlob(expected); err != nil {
		return storage.Blob{}, err
	}
	if content == nil {
		return storage.Blob{}, fmt.Errorf("%w: blob content is required", storage.ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return storage.Blob{}, err
	}
	target := s.blobPath(expected.SHA256)
	if _, err := os.Stat(target); err == nil {
		return s.verifyPath(ctx, target, expected)
	} else if !errors.Is(err, os.ErrNotExist) {
		return storage.Blob{}, fmt.Errorf("stat filesystem blob: %w", err)
	}

	temporary, err := os.CreateTemp(s.staging, "blob-*")
	if err != nil {
		return storage.Blob{}, fmt.Errorf("create filesystem blob staging file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(privateFile); err != nil {
		temporary.Close()
		return storage.Blob{}, fmt.Errorf("set filesystem blob staging permissions: %w", err)
	}

	hash := sha256.New()
	written, copyErr := copyExpected(ctx, io.MultiWriter(temporary, hash), content, expected.Size)
	if copyErr != nil {
		temporary.Close()
		return storage.Blob{}, copyErr
	}
	actualDigest := hex.EncodeToString(hash.Sum(nil))
	if written != expected.Size || actualDigest != expected.SHA256 {
		temporary.Close()
		return storage.Blob{}, fmt.Errorf("%w: expected size %d and SHA-256 %s, received size %d and SHA-256 %s", storage.ErrIntegrity, expected.Size, expected.SHA256, written, actualDigest)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return storage.Blob{}, fmt.Errorf("sync filesystem blob staging file: %w", err)
	}
	if err := temporary.Chmod(readOnlyFile); err != nil {
		temporary.Close()
		return storage.Blob{}, fmt.Errorf("set filesystem blob permissions: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return storage.Blob{}, fmt.Errorf("close filesystem blob staging file: %w", err)
	}

	if err := mkdirPrivate(filepath.Dir(target)); err != nil {
		return storage.Blob{}, fmt.Errorf("create filesystem blob directory: %w", err)
	}
	if err := os.Link(temporaryPath, target); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return storage.Blob{}, fmt.Errorf("atomically finalize filesystem blob: %w", err)
		}
		return s.verifyPath(ctx, target, expected)
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return storage.Blob{}, fmt.Errorf("sync filesystem blob directory: %w", err)
	}
	return s.verifyPath(ctx, target, expected)
}

func (s *Store) Stat(ctx context.Context, digest string) (storage.Blob, error) {
	if err := storage.ValidateSHA256(digest); err != nil {
		return storage.Blob{}, err
	}
	return s.verifyPath(ctx, s.blobPath(digest), storage.Blob{SHA256: digest, Size: -1})
}

func (s *Store) Open(ctx context.Context, digest string) (io.ReadCloser, error) {
	if _, err := s.Stat(ctx, digest); err != nil {
		return nil, err
	}
	file, err := s.openStoreFile(s.blobPath(digest))
	if errors.Is(err, os.ErrNotExist) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open filesystem blob: %w", err)
	}
	return file, nil
}

func (s *Store) WalkBlobs(ctx context.Context, visit func(storage.BlobMetadata) error) error {
	if visit == nil {
		return fmt.Errorf("%w: blob visitor is required", storage.ErrInvalid)
	}
	shards, err := os.ReadDir(s.blobs)
	if err != nil {
		return filesystemInventoryError(ctx, "enumerate blob shards", err)
	}
	for _, shard := range shards {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !isCanonicalShard(shard.Name()) || shard.Type()&os.ModeSymlink != 0 || !shard.IsDir() {
			return fmt.Errorf("%w: filesystem blob inventory is noncanonical", storage.ErrIntegrity)
		}
		files, err := os.ReadDir(filepath.Join(s.blobs, shard.Name()))
		if err != nil {
			return filesystemInventoryError(ctx, "enumerate blob shard", err)
		}
		for _, entry := range files {
			if err := ctx.Err(); err != nil {
				return err
			}
			digest := entry.Name()
			if storage.ValidateSHA256(digest) != nil || digest[:2] != shard.Name() || entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: filesystem blob inventory is noncanonical", storage.ErrIntegrity)
			}
			info, err := entry.Info()
			if err != nil {
				return filesystemInventoryError(ctx, "inspect blob metadata", err)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%w: filesystem blob inventory contains a non-file", storage.ErrIntegrity)
			}
			if err := visit(storage.BlobMetadata{SHA256: digest, Size: info.Size(), LastModified: info.ModTime().UTC()}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) DeleteBlobs(ctx context.Context, digests []string) error {
	if len(digests) > 1000 {
		return fmt.Errorf("%w: blob deletion batch exceeds 1000 entries", storage.ErrInvalid)
	}
	for _, digest := range digests {
		if err := storage.ValidateSHA256(digest); err != nil {
			return err
		}
	}
	touched := make(map[string]struct{})
	for _, digest := range digests {
		if err := ctx.Err(); err != nil {
			return err
		}
		blobPath := s.blobPath(digest)
		info, err := os.Lstat(blobPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return filesystemInventoryError(ctx, "inspect blob for deletion", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: filesystem blob deletion target is noncanonical", storage.ErrIntegrity)
		}
		if err := os.Remove(blobPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return filesystemInventoryError(ctx, "delete blob", err)
		}
		touched[filepath.Dir(blobPath)] = struct{}{}
	}
	for directory := range touched {
		if err := syncDirectory(directory); err != nil {
			return filesystemInventoryError(ctx, "sync blob deletion", err)
		}
	}
	return nil
}

func (s *Store) MaterializeRevision(ctx context.Context, revisionID string, manifest manageddata.Manifest) (manageddata.RevisionLease, error) {
	if err := validateRevisionID(revisionID); err != nil {
		return nil, err
	}
	if err := manifest.Validate(manageddata.Limits{}); err != nil || revisionID != manifest.RevisionID() {
		return nil, fmt.Errorf("%w: revision ID and manifest must be canonical and matching", storage.ErrInvalid)
	}
	files := manifest.Files
	if err := validateManifestPaths(files); err != nil {
		return nil, err
	}
	finalContainer := filepath.Join(s.revisions, revisionID)
	finalPath := filepath.Join(finalContainer, "data")
	if _, err := os.Lstat(finalContainer); err == nil {
		if err := s.verifyRevisionContainer(ctx, finalContainer, finalPath, files); err != nil {
			return nil, err
		}
		return staticRevisionLease{root: finalPath}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat filesystem revision: %w", err)
	}

	temporary, err := os.MkdirTemp(s.revisions, ".revision-*")
	if err != nil {
		return nil, fmt.Errorf("create filesystem revision staging directory: %w", err)
	}
	cleanup := func() {
		_ = makeTreeWritable(temporary)
		_ = os.RemoveAll(temporary)
	}
	defer cleanup()
	temporaryView := filepath.Join(temporary, "data")
	if err := os.Mkdir(temporaryView, privateDir); err != nil {
		return nil, fmt.Errorf("create filesystem revision data root: %w", err)
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		blob, err := s.Stat(ctx, file.SHA256)
		if err != nil {
			return nil, err
		}
		destination := filepath.Join(temporaryView, filepath.FromSlash(file.Path))
		if err := mkdirPrivate(filepath.Dir(destination)); err != nil {
			return nil, fmt.Errorf("create filesystem revision directory: %w", err)
		}
		if err := os.Link(s.blobPath(blob.SHA256), destination); err != nil {
			return nil, fmt.Errorf("link filesystem revision blob: %w", err)
		}
	}
	if err := makeTreeReadOnly(temporaryView); err != nil {
		return nil, fmt.Errorf("make filesystem revision immutable: %w", err)
	}
	if err := syncDirectory(temporary); err != nil {
		return nil, fmt.Errorf("sync filesystem revision staging directory: %w", err)
	}
	if err := os.Rename(temporary, finalContainer); err != nil {
		if _, statErr := os.Lstat(finalContainer); statErr == nil {
			if verifyErr := s.verifyRevisionContainer(ctx, finalContainer, finalPath, files); verifyErr != nil {
				return nil, verifyErr
			}
			return staticRevisionLease{root: finalPath}, nil
		}
		return nil, fmt.Errorf("atomically finalize filesystem revision: %w", err)
	}
	temporary = ""
	if err := syncDirectory(s.revisions); err != nil {
		return nil, fmt.Errorf("sync filesystem revision directory: %w", err)
	}
	return staticRevisionLease{root: finalPath}, nil
}

type staticRevisionLease struct{ root string }

func (l staticRevisionLease) Root() string { return l.root }
func (staticRevisionLease) Release() error { return nil }

func (s *Store) BlobPath(digest string) string {
	if storage.ValidateSHA256(digest) != nil {
		return ""
	}
	return s.blobPath(digest)
}

func (s *Store) blobPath(digest string) string {
	return filepath.Join(s.blobs, digest[:2], digest)
}

func isCanonicalShard(value string) bool {
	if len(value) != 2 {
		return false
	}
	for _, char := range []byte(value) {
		if char < '0' || char > '9' && char < 'a' || char > 'f' {
			return false
		}
	}
	return true
}

func filesystemInventoryError(ctx context.Context, operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return fmt.Errorf("%w: %s", storage.ErrBackend, operation)
}

func (s *Store) verifyPath(ctx context.Context, filePath string, expected storage.Blob) (storage.Blob, error) {
	file, err := s.openStoreFile(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return storage.Blob{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.Blob{}, fmt.Errorf("open filesystem blob for verification: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return storage.Blob{}, fmt.Errorf("stat filesystem blob: %w", err)
	}
	if !info.Mode().IsRegular() {
		return storage.Blob{}, fmt.Errorf("%w: filesystem blob is not a regular file", storage.ErrIntegrity)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, &contextReader{ctx: ctx, reader: file}); err != nil {
		return storage.Blob{}, fmt.Errorf("verify filesystem blob: %w", err)
	}
	actualDigest := hex.EncodeToString(hash.Sum(nil))
	if actualDigest != expected.SHA256 || expected.Size >= 0 && info.Size() != expected.Size {
		return storage.Blob{}, fmt.Errorf("%w: filesystem blob does not match its content address", storage.ErrIntegrity)
	}
	return storage.Blob{SHA256: actualDigest, Size: info.Size(), URI: (&url.URL{Scheme: "file", Path: filePath}).String()}, nil
}

// openStoreFile resolves an already-validated content address beneath the
// directory handle for this store. os.Root rejects symlink and traversal
// escapes even if an on-disk shard is replaced between validation and open.
func (s *Store) openStoreFile(filePath string) (*os.File, error) {
	relative, err := filepath.Rel(s.root, filePath)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: filesystem path escapes the blob store", storage.ErrIntegrity)
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(relative)
}

func (s *Store) verifyRevision(ctx context.Context, revisionPath string, files []manageddata.File) error {
	expected := make(map[string]manageddata.File, len(files))
	expectedDirectories := map[string]struct{}{".": {}}
	for _, file := range files {
		expected[file.Path] = file
		for parent := path.Dir(file.Path); parent != "."; parent = path.Dir(parent) {
			expectedDirectories[parent] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(files))
	err := filepath.WalkDir(revisionPath, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(revisionPath, current)
		if err != nil {
			return err
		}
		logicalPath := filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: filesystem revision path %q is a symlink", storage.ErrIntegrity, logicalPath)
		}
		if entry.IsDir() {
			if _, ok := expectedDirectories[logicalPath]; !ok {
				return fmt.Errorf("%w: filesystem revision contains unexpected directory %q", storage.ErrIntegrity, logicalPath)
			}
			if info.Mode().Perm() != readOnlyDir {
				return fmt.Errorf("%w: filesystem revision directory %q is not read-only", storage.ErrIntegrity, logicalPath)
			}
			return nil
		}
		expectedFile, ok := expected[logicalPath]
		if !ok {
			return fmt.Errorf("%w: filesystem revision contains unexpected path %q", storage.ErrIntegrity, logicalPath)
		}
		sourceInfo, err := os.Stat(s.blobPath(expectedFile.SHA256))
		sourceExists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: filesystem revision path %q is not a regular file", storage.ErrIntegrity, logicalPath)
		}
		if info.Mode().Perm() != readOnlyFile {
			return fmt.Errorf("%w: filesystem revision path %q is not read-only", storage.ErrIntegrity, logicalPath)
		}
		if info.Size() != expectedFile.Size {
			return fmt.Errorf("%w: filesystem revision path %q has the wrong size", storage.ErrIntegrity, logicalPath)
		}
		if sourceExists && !os.SameFile(sourceInfo, info) {
			return fmt.Errorf("%w: filesystem revision path %q is not linked to its blob", storage.ErrIntegrity, logicalPath)
		}
		viewFile, err := os.Open(current)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, &contextReader{ctx: ctx, reader: viewFile})
		closeErr := viewFile.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if hex.EncodeToString(hash.Sum(nil)) != expectedFile.SHA256 {
			return fmt.Errorf("%w: filesystem revision path %q does not match its content address", storage.ErrIntegrity, logicalPath)
		}
		seen[logicalPath] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("%w: filesystem revision is missing files", storage.ErrIntegrity)
	}
	return nil
}

func (s *Store) verifyRevisionContainer(ctx context.Context, container, root string, files []manageddata.File) error {
	info, err := os.Lstat(container)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != privateDir {
		return fmt.Errorf("%w: filesystem revision envelope is invalid", storage.ErrIntegrity)
	}
	entries, err := os.ReadDir(container)
	if err != nil {
		return fmt.Errorf("%w: inspect filesystem revision envelope", storage.ErrIntegrity)
	}
	if len(entries) != 1 || entries[0].Name() != "data" || !entries[0].IsDir() || entries[0].Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: filesystem revision envelope must contain only its data root", storage.ErrIntegrity)
	}
	return s.verifyRevision(ctx, root, files)
}

func copyExpected(ctx context.Context, destination io.Writer, source io.Reader, expectedSize int64) (int64, error) {
	reader := &contextReader{ctx: ctx, reader: source}
	if expectedSize < int64(^uint64(0)>>1) {
		reader.reader = io.LimitReader(reader.reader, expectedSize+1)
	}
	written, err := io.Copy(destination, reader)
	if err != nil {
		return written, fmt.Errorf("write filesystem blob: %w", err)
	}
	return written, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func validateRevisionID(value string) error {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("%w: revision ID must use the sha256 scheme", storage.ErrInvalid)
	}
	if err := storage.ValidateSHA256(strings.TrimPrefix(value, prefix)); err != nil {
		return fmt.Errorf("%w: revision ID must contain a canonical SHA-256 digest", storage.ErrInvalid)
	}
	return nil
}

func validateManifestPaths(files []manageddata.File) error {
	seen := make(map[string]string, len(files))
	for _, file := range files {
		if err := storage.ValidateSHA256(file.SHA256); err != nil {
			return err
		}
		if file.Path == "" || strings.Contains(file.Path, "\\") || path.IsAbs(file.Path) || path.Clean(file.Path) != file.Path || file.Path == "." || file.Path == ".." || strings.HasPrefix(file.Path, "../") {
			return fmt.Errorf("%w: revision path %q is not a canonical relative path", storage.ErrInvalid, file.Path)
		}
		folded := strings.ToLower(file.Path)
		if previous, ok := seen[folded]; ok {
			return fmt.Errorf("%w: revision path collision between %q and %q", storage.ErrInvalid, previous, file.Path)
		}
		seen[folded] = file.Path
	}
	return nil
}

func mkdirPrivate(directory string) error {
	if err := os.MkdirAll(directory, privateDir); err != nil {
		return err
	}
	return os.Chmod(directory, privateDir)
}

func makeTreeReadOnly(root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, current)
			return nil
		}
		return os.Chmod(current, readOnlyFile)
	}); err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
	for _, directory := range directories {
		if err := os.Chmod(directory, readOnlyDir); err != nil {
			return err
		}
	}
	return nil
}

func makeTreeWritable(root string) error {
	if root == "" {
		return nil
	}
	return filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(current, privateDir)
		}
		return nil
	})
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

var _ storage.BlobInventory = (*Store)(nil)
var _ manageddata.RevisionMaterializer = (*Store)(nil)
