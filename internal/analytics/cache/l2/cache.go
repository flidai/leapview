// Package l2 implements the optional node-local result cache.
//
// The cache is deliberately disposable. Result bytes are immutable,
// content-addressed files and SQLite contains only a rebuildable lookup index.
// Nothing in this package is an authority or a source of durable workflow,
// session, event, audit, delivery, lineage, or lease state.
package l2

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

const (
	formatVersion = 1
	objectDir     = "objects"
	indexName     = "index.sqlite"
	maxNameBytes  = 512
)

var sha256Identity = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var (
	ErrInvalid     = errors.New("invalid L2 cache input")
	ErrClosed      = errors.New("L2 cache is closed")
	ErrOversized   = errors.New("L2 cache entry exceeds byte limit")
	ErrCorrupt     = errors.New("L2 cache entry is corrupt")
	ErrUnavailable = errors.New("L2 cache is unavailable")
)

// Key is the complete L2 isolation boundary. The same result key in another
// namespace, format version, or storage-security domain is a different entry.
// KeyDigest must be the digest returned by analytics/cache.Key.Digest().
type Key struct {
	Namespace      string
	Version        int
	SecurityDomain string
	KeyDigest      string
}

// NewKey constructs and validates an L2 key from an existing result-cache key.
func NewKey(namespace string, version int, securityDomain, keyDigest string) (Key, error) {
	k := Key{Namespace: namespace, Version: version, SecurityDomain: securityDomain, KeyDigest: keyDigest}
	if err := k.Validate(); err != nil {
		return Key{}, err
	}
	return k, nil
}

func (k Key) Validate() error {
	if strings.TrimSpace(k.Namespace) == "" || len(k.Namespace) > maxNameBytes || strings.ContainsRune(k.Namespace, '\x00') {
		return fmt.Errorf("%w: namespace must be non-empty and bounded", ErrInvalid)
	}
	if k.Version <= 0 {
		return fmt.Errorf("%w: version must be positive", ErrInvalid)
	}
	if strings.TrimSpace(k.SecurityDomain) == "" || len(k.SecurityDomain) > maxNameBytes || strings.ContainsRune(k.SecurityDomain, '\x00') {
		return fmt.Errorf("%w: security domain must be non-empty and bounded", ErrInvalid)
	}
	if !sha256Identity.MatchString(k.KeyDigest) {
		return fmt.Errorf("%w: key digest must be a lowercase sha256 identity", ErrInvalid)
	}
	return nil
}

// IdentityDigest is a digest of the full isolation key, not an authorization
// decision. It is only used as a local SQLite key and sidecar filename.
func (k Key) IdentityDigest() string {
	b, _ := json.Marshal(struct {
		Namespace      string `json:"namespace"`
		Version        int    `json:"version"`
		SecurityDomain string `json:"security_domain"`
		KeyDigest      string `json:"key_digest"`
	}{k.Namespace, k.Version, k.SecurityDomain, k.KeyDigest})
	sum := sha256.Sum256(append([]byte("flid.analytics.l2.key.v1\x00"), b...))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type Entry struct {
	Key          Key
	ObjectDigest string
	Size         int64
	ExpiresAt    *time.Time
}

// Config controls the disposable cache. Root is created if absent. MaxEntries
// and MaxBytes must both be positive. Clock is injectable for deterministic
// expiry and eviction tests.
type Config struct {
	Root       string
	MaxEntries int
	MaxBytes   int64
	Clock      func() time.Time
	SyncDirs   bool
}

// Metrics is a point-in-time copy of cache counters and state.
type Metrics struct {
	Hits            uint64
	Misses          uint64
	BytesRead       uint64
	BytesWritten    uint64
	Entries         int
	Bytes           int64
	Evictions       uint64
	Corruption      uint64
	Rebuilds        uint64
	Reconciliations uint64
	Rebuilding      bool
	Ready           bool
}

type sidecar struct {
	FormatVersion  int    `json:"format_version"`
	Namespace      string `json:"namespace"`
	Version        int    `json:"version"`
	SecurityDomain string `json:"security_domain"`
	KeyDigest      string `json:"key_digest"`
	ObjectDigest   string `json:"object_digest"`
	Size           int64  `json:"size"`
	ExpiresAt      *int64 `json:"expires_at_unix_nano,omitempty"`
}

type Cache struct {
	mu         sync.RWMutex
	root       string
	objects    string
	index      string
	db         *sql.DB
	maxEnt     int
	maxBytes   int64
	clock      func() time.Time
	syncDirs   bool
	closed     bool
	ready      bool
	metrics    metricsCounters
	seq        atomic.Int64
	rebuilding atomic.Bool
}

type metricsCounters struct {
	hits, misses, bytesRead, bytesWritten, evictions, corruption, rebuilds, reconciliations atomic.Uint64
}

// New opens (or creates) an L2 cache and reconciles its disposable index and
// immutable object/sidecar files before returning.
func New(cfg Config) (*Cache, error) {
	if cfg.Root == "" {
		return nil, fmt.Errorf("%w: root is required", ErrInvalid)
	}
	if cfg.MaxEntries <= 0 || cfg.MaxBytes <= 0 {
		return nil, fmt.Errorf("%w: positive limits are required", ErrInvalid)
	}
	root, err := filepath.Abs(filepath.Clean(cfg.Root))
	if err != nil {
		return nil, fmt.Errorf("%w: root: %v", ErrInvalid, err)
	}
	if err := os.MkdirAll(filepath.Join(root, objectDir), 0o700); err != nil {
		return nil, fmt.Errorf("%w: create root: %v", ErrUnavailable, err)
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	c := &Cache{root: root, objects: filepath.Join(root, objectDir), index: filepath.Join(root, indexName), maxEnt: cfg.MaxEntries, maxBytes: cfg.MaxBytes, clock: clock, syncDirs: cfg.SyncDirs}
	_, indexStatErr := os.Stat(c.index)
	if err := c.openIndex(); err != nil {
		return nil, err
	}
	if errors.Is(indexStatErr, os.ErrNotExist) {
		c.metrics.rebuilds.Add(1)
	}
	c.rebuilding.Store(true)
	if err := c.reconcile(); err != nil {
		c.rebuilding.Store(false)
		_ = c.db.Close()
		return nil, err
	}
	c.rebuilding.Store(false)
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	return c, nil
}

func (c *Cache) openIndex() error {
	db, err := sql.Open("sqlite", c.index+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return fmt.Errorf("%w: open index: %v", ErrUnavailable, err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS entries (
		identity TEXT PRIMARY KEY,
		namespace TEXT NOT NULL,
		version INTEGER NOT NULL,
		security_domain TEXT NOT NULL,
		key_digest TEXT NOT NULL,
		object_digest TEXT NOT NULL,
		size INTEGER NOT NULL,
		expires_at INTEGER,
		last_access INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	)`); err != nil {
		_ = db.Close()
		return c.resetIndex(err)
	}
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM pragma_table_info('entries') WHERE name IN ('identity','namespace','version','security_domain','key_digest','object_digest','size','expires_at','last_access','created_at')`).Scan(&n); err != nil || n != 10 {
		_ = db.Close()
		return c.resetIndex(fmt.Errorf("invalid index schema"))
	}
	c.db = db
	return nil
}

func (c *Cache) resetIndex(cause error) error {
	c.metrics.rebuilds.Add(1)
	if c.db != nil {
		_ = c.db.Close()
		c.db = nil
	}
	backup := c.index + ".corrupt-" + fmt.Sprintf("%d", c.clock().UnixNano())
	if err := os.Rename(c.index, backup); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: reset index (%v): %v", ErrUnavailable, cause, err)
	}
	db, err := sql.Open("sqlite", c.index+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return fmt.Errorf("%w: reopen index: %v", ErrUnavailable, err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE entries (
		identity TEXT PRIMARY KEY, namespace TEXT NOT NULL, version INTEGER NOT NULL,
		security_domain TEXT NOT NULL, key_digest TEXT NOT NULL, object_digest TEXT NOT NULL,
		size INTEGER NOT NULL, expires_at INTEGER, last_access INTEGER NOT NULL, created_at INTEGER NOT NULL)`); err != nil {
		_ = db.Close()
		return fmt.Errorf("%w: recreate index: %v", ErrUnavailable, err)
	}
	c.db = db
	return nil
}

func (c *Cache) ensureOpen() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed || c.db == nil {
		return ErrClosed
	}
	return nil
}

func (c *Cache) openDB() (*sql.DB, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed || c.db == nil {
		return nil, ErrClosed
	}
	return c.db, nil
}

// Get returns a copy of an entry's bytes. Missing, expired, missing, and
// corrupt files are all cache misses and are self-healed where possible.
func (c *Cache) Get(ctx context.Context, key Key) ([]byte, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := key.Validate(); err != nil {
		return nil, false, err
	}
	db, err := c.openDB()
	if err != nil {
		return nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	identity := key.IdentityDigest()
	var row sidecar
	var expires sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT namespace,version,security_domain,key_digest,object_digest,size,expires_at FROM entries WHERE identity=?`, identity).Scan(&row.Namespace, &row.Version, &row.SecurityDomain, &row.KeyDigest, &row.ObjectDigest, &row.Size, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		c.metrics.misses.Add(1)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: index lookup: %v", ErrUnavailable, err)
	}
	row.FormatVersion = formatVersion
	if expires.Valid {
		v := expires.Int64
		row.ExpiresAt = &v
	}
	if err := c.validateSidecar(key, row); err != nil || row.Size > c.maxBytes {
		c.metrics.corruption.Add(1)
		_ = c.removeIdentityWithDB(ctx, db, identity, row.ObjectDigest, key.SecurityDomain)
		c.metrics.misses.Add(1)
		return nil, false, nil
	}
	if row.ExpiresAt != nil && c.clock().UnixNano() >= *row.ExpiresAt {
		_ = c.removeIdentityWithDB(ctx, db, identity, row.ObjectDigest, key.SecurityDomain)
		c.metrics.misses.Add(1)
		return nil, false, nil
	}
	path := c.objectPath(key.SecurityDomain, row.ObjectDigest)
	data, err := readAndValidate(path, row.ObjectDigest, row.Size)
	if err != nil {
		c.metrics.corruption.Add(1)
		_ = c.removeIdentityWithDB(ctx, db, identity, row.ObjectDigest, key.SecurityDomain)
		c.metrics.misses.Add(1)
		return nil, false, nil
	}
	if sc, err := c.readSidecar(key.SecurityDomain, row.ObjectDigest, identity); err != nil || c.validateSidecar(key, sc) != nil || sc.Size != row.Size || !sameExpiry(sc.ExpiresAt, row.ExpiresAt) {
		c.metrics.corruption.Add(1)
		_ = c.removeIdentityWithDB(ctx, db, identity, row.ObjectDigest, key.SecurityDomain)
		c.metrics.misses.Add(1)
		return nil, false, nil
	}
	_, _ = db.ExecContext(ctx, `UPDATE entries SET last_access=? WHERE identity=?`, c.nextAccess(), identity)
	c.metrics.hits.Add(1)
	c.metrics.bytesRead.Add(uint64(len(data)))
	return data, true, nil
}

// Put publishes immutable bytes and associates them with key. expiresAt is
// optional and variadic to make an unexpired Put concise.
func (c *Cache) Put(ctx context.Context, key Key, data []byte, expiresAt ...*time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(expiresAt) > 1 {
		return fmt.Errorf("%w: at most one expiry", ErrInvalid)
	}
	if err := key.Validate(); err != nil {
		return err
	}
	db, err := c.openDB()
	if err != nil {
		return err
	}
	if int64(len(data)) > c.maxBytes {
		return ErrOversized
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	objSum := sha256.Sum256(data)
	objDigest := "sha256:" + hex.EncodeToString(objSum[:])
	identity := key.IdentityDigest()
	if err := c.publishObject(ctx, key.SecurityDomain, objDigest, data); err != nil {
		return err
	}
	var expiry *int64
	if len(expiresAt) == 1 && expiresAt[0] != nil {
		v := expiresAt[0].UnixNano()
		expiry = &v
	}
	sc := sidecar{FormatVersion: formatVersion, Namespace: key.Namespace, Version: key.Version, SecurityDomain: key.SecurityDomain, KeyDigest: key.KeyDigest, ObjectDigest: objDigest, Size: int64(len(data)), ExpiresAt: expiry}
	if err := c.writeSidecar(ctx, key.SecurityDomain, objDigest, identity, sc); err != nil {
		return err
	}
	now := c.nextAccess()
	if _, err := db.ExecContext(ctx, `INSERT INTO entries(identity,namespace,version,security_domain,key_digest,object_digest,size,expires_at,last_access,created_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(identity) DO UPDATE SET namespace=excluded.namespace,version=excluded.version,security_domain=excluded.security_domain,key_digest=excluded.key_digest,object_digest=excluded.object_digest,size=excluded.size,expires_at=excluded.expires_at,last_access=excluded.last_access`, identity, key.Namespace, key.Version, key.SecurityDomain, key.KeyDigest, objDigest, len(data), expiry, now, now); err != nil {
		return fmt.Errorf("%w: index publish: %v", ErrUnavailable, err)
	}
	c.metrics.bytesWritten.Add(uint64(len(data)))
	return c.evict(ctx, db)
}

func (c *Cache) publishObject(ctx context.Context, domain, digest string, data []byte) error {
	dir := c.objectDomainPath(domain)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: create object directory: %v", ErrUnavailable, err)
	}
	path := c.objectPath(domain, digest)
	if existing, err := readAndValidate(path, digest, int64(len(data))); err == nil {
		if !equalBytes(existing, data) {
			return fmt.Errorf("%w: digest path contains mismatched bytes", ErrCorrupt)
		}
		return nil
	}
	tmp, err := os.CreateTemp(dir, ".object-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: create object temporary: %v", ErrUnavailable, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }
	if _, err = tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("%w: write object: %v", ErrUnavailable, err)
	}
	if err = tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("%w: sync object: %v", ErrUnavailable, err)
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("%w: close object: %v", ErrUnavailable, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		if _, validErr := readAndValidate(path, digest, int64(len(data))); validErr == nil {
			return nil // another process won publication of the same immutable object
		}
		return fmt.Errorf("%w: publish object: %v", ErrUnavailable, err)
	}
	return c.syncDirectory(dir)
}

func (c *Cache) writeSidecar(ctx context.Context, domain, objectDigest, identity string, sc sidecar) error {
	dir := c.objectDomainPath(domain)
	b, err := json.Marshal(sc)
	if err != nil {
		return fmt.Errorf("%w: marshal sidecar: %v", ErrInvalid, err)
	}
	tmp, err := os.CreateTemp(dir, ".sidecar-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: create sidecar temporary: %v", ErrUnavailable, err)
	}
	name := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(name) }
	if _, err = tmp.Write(b); err != nil {
		cleanup()
		return fmt.Errorf("%w: write sidecar: %v", ErrUnavailable, err)
	}
	if err = tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("%w: sync sidecar: %v", ErrUnavailable, err)
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("%w: close sidecar: %v", ErrUnavailable, err)
	}
	path := c.sidecarPath(domain, objectDigest, identity)
	if err = os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		if _, readErr := c.readSidecar(domain, objectDigest, identity); readErr == nil {
			return nil
		}
		return fmt.Errorf("%w: publish sidecar: %v", ErrUnavailable, err)
	}
	return c.syncDirectory(dir)
}

func (c *Cache) evict(ctx context.Context, db *sql.DB) error {
	for {
		var count int
		var bytes int64
		if err := db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(size),0) FROM entries`).Scan(&count, &bytes); err != nil {
			return fmt.Errorf("%w: eviction accounting: %v", ErrUnavailable, err)
		}
		if count <= c.maxEnt && bytes <= c.maxBytes {
			return nil
		}
		var identity, digest, domain string
		err := db.QueryRowContext(ctx, `SELECT identity,object_digest,security_domain FROM entries ORDER BY last_access ASC, identity ASC LIMIT 1`).Scan(&identity, &digest, &domain)
		if err != nil {
			return fmt.Errorf("%w: eviction selection: %v", ErrUnavailable, err)
		}
		if err := c.removeIdentityWithDB(ctx, db, identity, digest, domain); err != nil {
			return err
		}
		c.metrics.evictions.Add(1)
	}
}

func (c *Cache) removeIdentityWithDB(ctx context.Context, db *sql.DB, identity, digest, domain string) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM entries WHERE identity=?`, identity); err != nil {
		return err
	}
	_ = os.Remove(c.sidecarPath(domain, digest, identity))
	var refs int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM entries WHERE security_domain=? AND object_digest=?`, domain, digest).Scan(&refs); err == nil && refs == 0 {
		_ = os.Remove(c.objectPath(domain, digest))
	}
	return nil
}

func (c *Cache) reconcile() error {
	rows, err := c.db.Query(`SELECT identity,namespace,version,security_domain,key_digest,object_digest,size,expires_at FROM entries`)
	if err != nil {
		if resetErr := c.resetIndex(err); resetErr != nil {
			return resetErr
		}
		rows, err = c.db.Query(`SELECT identity,namespace,version,security_domain,key_digest,object_digest,size,expires_at FROM entries`)
		if err != nil {
			return fmt.Errorf("%w: query rebuilt index: %v", ErrUnavailable, err)
		}
	}
	indexed := make(map[string]struct{})
	referenced := make(map[string]struct{})
	invalid := make([]struct {
		identity string
		expired  bool
	}, 0)
	for rows.Next() {
		var identity, namespace, domain, keyDigest, digest string
		var version int
		var size int64
		var expires sql.NullInt64
		if err := rows.Scan(&identity, &namespace, &version, &domain, &keyDigest, &digest, &size, &expires); err != nil {
			_ = rows.Close()
			return err
		}
		key, keyErr := NewKey(namespace, version, domain, keyDigest)
		valid := keyErr == nil && key.IdentityDigest() == identity && sha256Identity.MatchString(digest)
		if valid {
			sc, scErr := c.readSidecar(domain, digest, identity)
			valid = scErr == nil && c.validateSidecar(key, sc) == nil && sc.Size == size && sameExpiry(sc.ExpiresAt, nullableInt64(expires)) && size <= c.maxBytes && readAndValidateMust(c.objectPath(domain, digest), digest, size)
			if expires.Valid && c.clock().UnixNano() >= expires.Int64 {
				valid = false
			}
		}
		if !valid {
			expired := expires.Valid && c.clock().UnixNano() >= expires.Int64
			invalid = append(invalid, struct {
				identity string
				expired  bool
			}{identity: identity, expired: expired})
			if !expired {
				c.metrics.corruption.Add(1)
			}
			continue
		}
		indexed[identity] = struct{}{}
		referenced[c.objectPath(domain, digest)] = struct{}{}
	}
	_ = rows.Close()
	for _, item := range invalid {
		_, _ = c.db.Exec(`DELETE FROM entries WHERE identity=?`, item.identity)
	}
	var maxAccess sql.NullInt64
	if err := c.db.QueryRow(`SELECT max(last_access) FROM entries`).Scan(&maxAccess); err == nil && maxAccess.Valid {
		c.seq.Store(maxAccess.Int64)
	}
	err = filepath.WalkDir(c.objects, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		domainDir := filepath.Base(filepath.Dir(path))
		digest, identity, ok := parseSidecarName(d.Name())
		if !ok {
			return nil
		}
		sc, scErr := readSidecarFile(path)
		if scErr != nil {
			c.metrics.corruption.Add(1)
			_ = os.Remove(path)
			return nil
		}
		key, keyErr := NewKey(sc.Namespace, sc.Version, sc.SecurityDomain, sc.KeyDigest)
		expired := sc.ExpiresAt != nil && c.clock().UnixNano() >= *sc.ExpiresAt
		if keyErr != nil || key.IdentityDigest() != identity || sc.ObjectDigest != digest || sc.FormatVersion != formatVersion || sc.Size < 0 || sc.Size > c.maxBytes || expired || readAndValidateMust(c.objectPath(sc.SecurityDomain, digest), digest, sc.Size) == false {
			if !expired {
				c.metrics.corruption.Add(1)
			}
			_ = os.Remove(path)
			return nil
		}
		if expected := filepath.Base(c.objectDomainPath(sc.SecurityDomain)); expected != domainDir {
			c.metrics.corruption.Add(1)
			_ = os.Remove(path)
			return nil
		}
		referenced[c.objectPath(sc.SecurityDomain, digest)] = struct{}{}
		if _, exists := indexed[identity]; !exists {
			access := c.nextAccess()
			_, err := c.db.Exec(`INSERT INTO entries(identity,namespace,version,security_domain,key_digest,object_digest,size,expires_at,last_access,created_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(identity) DO UPDATE SET namespace=excluded.namespace,version=excluded.version,security_domain=excluded.security_domain,key_digest=excluded.key_digest,object_digest=excluded.object_digest,size=excluded.size,expires_at=excluded.expires_at`, identity, sc.Namespace, sc.Version, sc.SecurityDomain, sc.KeyDigest, sc.ObjectDigest, sc.Size, sc.ExpiresAt, access, access)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: reconcile files: %v", ErrUnavailable, err)
	}
	// Content objects without a valid sidecar/index association are publication
	// leftovers (for example a process crash between object and sidecar rename).
	// Reclaim them so orphan bytes cannot defeat the configured disk bound.
	_ = filepath.WalkDir(c.objects, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".data") {
			return walkErr
		}
		if _, ok := referenced[path]; !ok {
			_ = os.Remove(path)
		}
		return nil
	})
	if err := c.evict(context.Background(), c.db); err != nil {
		return err
	}
	c.metrics.reconciliations.Add(1)
	return nil
}

func (c *Cache) nextAccess() int64 {
	return c.seq.Add(1)
}

func (c *Cache) validateSidecar(key Key, sc sidecar) error {
	if sc.FormatVersion != formatVersion || sc.Namespace != key.Namespace || sc.Version != key.Version || sc.SecurityDomain != key.SecurityDomain || sc.KeyDigest != key.KeyDigest || !sha256Identity.MatchString(sc.ObjectDigest) || sc.Size < 0 {
		return ErrCorrupt
	}
	return nil
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	v := value.Int64
	return &v
}

func sameExpiry(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func readAndValidateMust(path, digest string, size int64) bool {
	_, err := readAndValidate(path, digest, size)
	return err == nil
}

func readAndValidate(path, digest string, size int64) ([]byte, error) {
	if size < 0 {
		return nil, ErrCorrupt
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() != size {
		return nil, ErrCorrupt
	}
	h := sha256.New()
	data, err := io.ReadAll(io.TeeReader(f, h))
	if err != nil || int64(len(data)) != size {
		return nil, ErrCorrupt
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != digest {
		return nil, ErrCorrupt
	}
	return data, nil
}

func equalBytes(a, b []byte) bool { return len(a) == len(b) && string(a) == string(b) }

func readSidecarFile(path string) (sidecar, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return sidecar{}, err
	}
	var sc sidecar
	if err := json.Unmarshal(b, &sc); err != nil {
		return sidecar{}, err
	}
	return sc, nil
}

func (c *Cache) readSidecar(domain, digest, identity string) (sidecar, error) {
	return readSidecarFile(c.sidecarPath(domain, digest, identity))
}

func (c *Cache) objectDomainPath(domain string) string {
	sum := sha256.Sum256([]byte("flid.analytics.l2.domain.v1\x00" + domain))
	return filepath.Join(c.objects, hex.EncodeToString(sum[:]))
}
func (c *Cache) objectPath(domain, digest string) string {
	if !sha256Identity.MatchString(digest) {
		return filepath.Join(c.objects, ".invalid-object")
	}
	return filepath.Join(c.objectDomainPath(domain), digest[len("sha256:"):]+".data")
}
func (c *Cache) sidecarPath(domain, digest, identity string) string {
	if !sha256Identity.MatchString(digest) || !sha256Identity.MatchString(identity) {
		return filepath.Join(c.objects, ".invalid-sidecar")
	}
	return filepath.Join(c.objectDomainPath(domain), digest[len("sha256:"):]+"."+identity[len("sha256:"):]+".json")
}
func parseSidecarName(name string) (digest, identity string, ok bool) {
	parts := strings.Split(strings.TrimSuffix(name, ".json"), ".")
	if len(parts) != 2 || len(parts[0]) != 64 || len(parts[1]) != 64 {
		return "", "", false
	}
	return "sha256:" + parts[0], "sha256:" + parts[1], true
}

func (c *Cache) syncDirectory(dir string) error {
	if !c.syncDirs {
		return nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("%w: open directory for sync: %v", ErrUnavailable, err)
	}
	err = f.Sync()
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("%w: sync directory: %v", ErrUnavailable, err)
	}
	return nil
}

// Metrics returns counters plus current SQLite accounting.
func (c *Cache) Metrics() Metrics {
	m := Metrics{Hits: c.metrics.hits.Load(), Misses: c.metrics.misses.Load(), BytesRead: c.metrics.bytesRead.Load(), BytesWritten: c.metrics.bytesWritten.Load(), Evictions: c.metrics.evictions.Load(), Corruption: c.metrics.corruption.Load(), Rebuilds: c.metrics.rebuilds.Load(), Reconciliations: c.metrics.reconciliations.Load()}
	c.mu.RLock()
	m.Ready = c.ready && !c.closed
	m.Rebuilding = c.rebuilding.Load()
	db := c.db
	c.mu.RUnlock()
	if db != nil {
		_ = db.QueryRow(`SELECT count(*),coalesce(sum(size),0) FROM entries`).Scan(&m.Entries, &m.Bytes)
	}
	return m
}

// Reconcile revalidates the disposable index and imports valid sidecars that
// are absent from SQLite. It is safe to call after an operator removes cache
// files or replaces the index while the process is running.
func (c *Cache) Reconcile(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.ensureOpen(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.db == nil {
		return ErrClosed
	}
	if _, err := os.Stat(c.index); errors.Is(err, os.ErrNotExist) {
		if err := c.resetIndex(err); err != nil {
			return err
		}
	}
	c.rebuilding.Store(true)
	defer c.rebuilding.Store(false)
	return c.reconcile()
}

// IndexPath returns the disposable SQLite index path for diagnostics/tests.
func (c *Cache) IndexPath() string {
	if c == nil {
		return ""
	}
	return c.index
}

// Close cleanly closes SQLite. Published immutable files remain disposable
// state and may be removed independently without affecting correctness.
func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.ready = false
	db := c.db
	c.db = nil
	c.mu.Unlock()
	if db != nil {
		return db.Close()
	}
	return nil
}

// ObjectPath is exposed for diagnostics and tests; callers must treat the
// returned path as opaque and never construct it themselves.
func (c *Cache) ObjectPath(key Key, objectDigest string) (string, error) {
	if err := key.Validate(); err != nil || !sha256Identity.MatchString(objectDigest) {
		return "", fmt.Errorf("%w: invalid object path", ErrInvalid)
	}
	return c.objectPath(key.SecurityDomain, objectDigest), nil
}

// SortedObjectFiles returns immutable files in deterministic path order.
func (c *Cache) SortedObjectFiles() ([]string, error) {
	var paths []string
	err := filepath.WalkDir(c.objects, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".data") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}
