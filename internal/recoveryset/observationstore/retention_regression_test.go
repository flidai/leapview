package observationstore

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestPersistFrontierRejectsHorizonShorterThanDescriptor(t *testing.T) {
	provider := newVersionedFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, captured, evidence, descriptorUntil := savedEmptyEvidenceWithClient(t, provider, provider.fakeS3, now)
	set := regressionV2Set(t, captured.Boundary(), "sha256:"+evidence.Manifest.SHA256, mustDigest(t, captured.Boundary()), "sha256:"+evidence.Descriptor.SHA256)

	requestedUntil := descriptorUntil.Add(-time.Hour)
	if _, err := store.PersistFrontier(t.Context(), set, evidence, requestedUntil); !errors.Is(err, ErrRetention) {
		t.Fatalf("PersistFrontier accepted horizon before descriptor horizon: %v", err)
	}
	provider.mu.Lock()
	var frontierKey string
	for key := range provider.objects {
		if strings.HasPrefix(key, store.evidencePrefix()+"/frontiers/") {
			frontierKey = key
			break
		}
	}
	provider.mu.Unlock()
	if frontierKey != "" {
		t.Fatalf("short PersistFrontier wrote frontier object %q", frontierKey)
	}
	frontier, err := store.PersistFrontier(t.Context(), set, evidence, descriptorUntil)
	if err != nil {
		t.Fatalf("PersistFrontier rejected matching descriptor horizon: %v", err)
	}
	if got, err := store.ReadFrontier(t.Context(), frontier); err != nil || got.ID != set.ID {
		t.Fatalf("default reload of matching-horizon frontier: got=%#v err=%v", got, err)
	}
}

func TestReloadUsesRecordedHorizonWhenOptionalHorizonIsShorter(t *testing.T) {
	provider := newVersionedFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, _, evidence, descriptorUntil := savedEmptyEvidenceWithClient(t, provider, provider.fakeS3, now)

	// The descriptor is authoritative. A manifest retained only through an
	// optional, shorter request must not be accepted by Reload.
	provider.mu.Lock()
	manifest := provider.objects[evidence.Manifest.Key]
	manifest.retain = now.Add(time.Hour)
	provider.objects[evidence.Manifest.Key] = manifest
	provider.mu.Unlock()

	if _, err := store.Reload(t.Context(), evidence, descriptorUntil.Add(-time.Hour)); !errors.Is(err, ErrRetention) {
		t.Fatalf("Reload weakened descriptor horizon: %v", err)
	}
}

func TestReloadRejectsRetentionExpiryDuringDescriptorBodyRead(t *testing.T) {
	for _, tc := range []struct {
		name      string
		now       func(time.Time) time.Time
		wantError bool
	}{
		{name: "before deadline", now: func(until time.Time) time.Time { return until.Add(-time.Nanosecond) }},
		{name: "exact deadline", now: func(until time.Time) time.Time { return until }, wantError: true},
		{name: "after deadline", now: func(until time.Time) time.Time { return until.Add(time.Nanosecond) }, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := newVersionedFakeS3()
			start := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
			_, _, evidence, until := savedEmptyEvidenceWithClient(t, provider, provider.fakeS3, start)
			clock := start
			client := &retentionAdvancingClient{
				versionedFakeS3: provider,
				onRead: func(key string) {
					if key == evidence.Descriptor.Key {
						clock = tc.now(until)
					}
				},
			}
			store := newClockedRetentionStore(t, client, func() time.Time { return clock })

			_, err := store.Reload(t.Context(), evidence)
			if tc.wantError {
				if !errors.Is(err, ErrRetention) {
					t.Fatalf("Reload accepted retention expiry during descriptor read: %v", err)
				}
			} else if err != nil {
				t.Fatalf("Reload rejected protected evidence before deadline: %v", err)
			}
		})
	}
}

func TestReadFrontierRejectsRetentionExpiryAfterNestedReload(t *testing.T) {
	for _, tc := range []struct {
		name      string
		now       func(time.Time) time.Time
		wantError bool
	}{
		{name: "before deadline", now: func(until time.Time) time.Time { return until.Add(-time.Nanosecond) }},
		{name: "exact deadline", now: func(until time.Time) time.Time { return until }, wantError: true},
		{name: "after deadline", now: func(until time.Time) time.Time { return until.Add(time.Nanosecond) }, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := newVersionedFakeS3()
			start := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
			store, captured, evidence, until := savedEmptyEvidenceWithClient(t, provider, provider.fakeS3, start)
			set := regressionV2Set(t, captured.Boundary(), "sha256:"+evidence.Manifest.SHA256, mustDigest(t, captured.Boundary()), "sha256:"+evidence.Descriptor.SHA256)
			frontier, err := store.PersistFrontier(t.Context(), set, evidence, until)
			if err != nil {
				t.Fatalf("PersistFrontier() = %v", err)
			}

			clock := start
			client := &retentionAdvancingClient{
				versionedFakeS3: provider,
				onRead: func(key string) {
					// The set-ID body is read only after the nested Reload and
					// frontier body reads have completed.
					if key == frontier.SetIDObject.Key {
						clock = tc.now(until)
					}
				},
			}
			reader := newClockedRetentionStore(t, client, func() time.Time { return clock })

			got, err := reader.ReadFrontier(t.Context(), frontier)
			if tc.wantError {
				if !errors.Is(err, ErrRetention) {
					t.Fatalf("ReadFrontier accepted retention expiry after nested Reload: %v", err)
				}
			} else if err != nil || got.ID != set.ID {
				t.Fatalf("ReadFrontier rejected protected evidence before deadline: got=%#v err=%v", got, err)
			}
		})
	}
}

func newClockedRetentionStore(t *testing.T, client Client, clock func() time.Time) *Store {
	t.Helper()
	store, err := New(client, Config{
		Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", SourceBucket: "source", EvidenceBucket: "evidence", Prefix: "test", Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type retentionAdvancingClient struct {
	*versionedFakeS3
	onRead func(string)
}

func (c *retentionAdvancingClient) GetObject(ctx context.Context, in *s3.GetObjectInput, options ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	out, err := c.versionedFakeS3.GetObject(ctx, in, options...)
	if err != nil || out == nil || out.Body == nil || c.onRead == nil {
		return out, err
	}
	out.Body = &retentionAdvancingBody{ReadCloser: out.Body, key: stringValue(in.Key), onRead: c.onRead}
	return out, nil
}

type retentionAdvancingBody struct {
	io.ReadCloser
	key    string
	onRead func(string)
	once   sync.Once
}

func (b *retentionAdvancingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.once.Do(func() { b.onRead(b.key) })
	}
	return n, err
}
