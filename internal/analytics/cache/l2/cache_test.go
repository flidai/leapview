package l2

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testKey(t *testing.T, domain string) Key {
	t.Helper()
	return Key{Namespace: "arrow", Version: 1, SecurityDomain: domain, KeyDigest: "sha256:" + hex.EncodeToString(make([]byte, 32))}
}

func newTestCache(t *testing.T, root string, clock *time.Time, entries int, bytes int64) *Cache {
	t.Helper()
	c, err := New(Config{Root: root, MaxEntries: entries, MaxBytes: bytes, Clock: func() time.Time { return *clock }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestPutGetAndRebuild(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(100, 0)
	k := testKey(t, "tenant/a")
	c := newTestCache(t, root, &now, 4, 100)
	if err := c.Put(context.Background(), k, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, hit, err := c.Get(context.Background(), k)
	if err != nil || !hit || string(got) != "hello" {
		t.Fatalf("get = %q, %v, %v", got, hit, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = New(Config{Root: root, MaxEntries: 4, MaxBytes: 100, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got, hit, err = c.Get(context.Background(), k)
	if err != nil || !hit || string(got) != "hello" {
		t.Fatalf("rebuild get = %q, %v, %v", got, hit, err)
	}
}

func TestIsolationAndCorruptionMiss(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(100, 0)
	c := newTestCache(t, root, &now, 4, 100)
	k := testKey(t, "tenant/a")
	if err := c.Put(context.Background(), k, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	other := testKey(t, "tenant/b")
	if _, hit, err := c.Get(context.Background(), other); err != nil || hit {
		t.Fatalf("cross-domain lookup = hit=%v err=%v", hit, err)
	}
	digest := sha256.Sum256([]byte("hello"))
	path, _ := c.ObjectPath(k, "sha256:"+hex.EncodeToString(digest[:]))
	if err := os.WriteFile(path, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := c.Get(context.Background(), k); err != nil || hit {
		t.Fatalf("corrupt lookup = hit=%v err=%v", hit, err)
	}
	if c.Metrics().Corruption == 0 {
		t.Fatal("corruption was not counted")
	}
}

func TestDeterministicLRU(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(100, 0)
	c := newTestCache(t, root, &now, 2, 100)
	a, b, d := testKey(t, "a"), testKey(t, "b"), testKey(t, "d")
	if err := c.Put(context.Background(), a, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(context.Background(), b, []byte("b")); err != nil {
		t.Fatal(err)
	}
	if _, hit, _ := c.Get(context.Background(), a); !hit {
		t.Fatal("a should hit")
	}
	if err := c.Put(context.Background(), d, []byte("d")); err != nil {
		t.Fatal(err)
	}
	if _, hit, _ := c.Get(context.Background(), b); hit {
		t.Fatal("b should be evicted")
	}
	if _, hit, _ := c.Get(context.Background(), a); !hit {
		t.Fatal("a should remain")
	}
}

func TestConcurrentPublication(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(100, 0)
	c := newTestCache(t, root, &now, 4, 100)
	k := testKey(t, "a")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Put(context.Background(), k, []byte("same"))
		}()
	}
	wg.Wait()
	got, hit, err := c.Get(context.Background(), k)
	if err != nil || !hit || string(got) != "same" {
		t.Fatalf("concurrent get = %q %v %v", got, hit, err)
	}
}

func TestConcurrentPublicationAcrossCaches(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(100, 0)
	a := newTestCache(t, root, &now, 4, 100)
	b, err := New(Config{Root: root, MaxEntries: 4, MaxBytes: 100, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	k := testKey(t, "a")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		worker := a
		if i%2 != 0 {
			worker = b
		}
		go func(c *Cache) {
			defer wg.Done()
			_ = c.Put(context.Background(), k, []byte("same"))
		}(worker)
	}
	wg.Wait()
	if _, hit, err := a.Get(context.Background(), k); err != nil || !hit {
		t.Fatalf("cache A get = %v %v", hit, err)
	}
	if _, hit, err := b.Get(context.Background(), k); err != nil || !hit {
		t.Fatalf("cache B get = %v %v", hit, err)
	}
}

func TestExpiryIsMissNotCorruption(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(100, 0)
	c := newTestCache(t, root, &now, 4, 100)
	k := testKey(t, "a")
	exp := now.Add(time.Second)
	if err := c.Put(context.Background(), k, []byte("x"), &exp); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if _, hit, err := c.Get(context.Background(), k); err != nil || hit {
		t.Fatalf("expired get = %v %v", hit, err)
	}
	if c.Metrics().Corruption != 0 {
		t.Fatal("expiry counted as corruption")
	}
}

func TestDeleteRootIsMiss(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(100, 0)
	c := newTestCache(t, root, &now, 4, 100)
	k := testKey(t, "a")
	if err := c.Put(context.Background(), k, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "objects")); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{Root: root, MaxEntries: 4, MaxBytes: 100, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, hit, err := c.Get(context.Background(), k); err != nil || hit {
		t.Fatalf("deleted root get = %v %v", hit, err)
	}
}

func TestMissingIndexRebuildsFromSidecar(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(100, 0)
	k := testKey(t, "a")
	c := newTestCache(t, root, &now, 4, 100)
	if err := c.Put(context.Background(), k, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, indexName)); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{Root: root, MaxEntries: 4, MaxBytes: 100, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, hit, err := c.Get(context.Background(), k); err != nil || !hit {
		t.Fatalf("rebuilt get = %v %v", hit, err)
	}
	if c.Metrics().Rebuilds == 0 {
		t.Fatal("missing index rebuild was not counted")
	}
}

func TestCorruptIndexRebuilds(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(100, 0)
	k := testKey(t, "a")
	c := newTestCache(t, root, &now, 4, 100)
	if err := c.Put(context.Background(), k, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, indexName), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{Root: root, MaxEntries: 4, MaxBytes: 100, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, hit, err := c.Get(context.Background(), k); err != nil || !hit {
		t.Fatalf("corrupt rebuilt get = %v %v", hit, err)
	}
	if c.Metrics().Rebuilds == 0 {
		t.Fatal("corrupt index rebuild was not counted")
	}
}

func TestReconcileAfterOpenIndexRemoval(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(100, 0)
	k := testKey(t, "a")
	c := newTestCache(t, root, &now, 4, 100)
	if err := c.Put(context.Background(), k, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(c.IndexPath()); err != nil {
		t.Fatal(err)
	}
	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := c.Get(context.Background(), k); err != nil || !hit {
		t.Fatalf("reconciled open get = %v %v", hit, err)
	}
}

func TestMalformedIndexDigestIsMissWithoutPanic(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(100, 0)
	k := testKey(t, "a")
	c := newTestCache(t, root, &now, 4, 100)
	if err := c.Put(context.Background(), k, []byte("x")); err != nil {
		t.Fatal(err)
	}
	index, err := sql.Open("sqlite", c.IndexPath())
	if err != nil {
		t.Fatal(err)
	}
	_, err = index.Exec(`UPDATE entries SET object_digest='bad' WHERE identity=?`, k.IdentityDigest())
	_ = index.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, hit, err := c.Get(context.Background(), k); err != nil || hit {
		t.Fatalf("malformed digest get = %v %v", hit, err)
	}
}
