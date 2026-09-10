package storage_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata/storage"
)

func TestProviderVersionObservationSetIsIdempotentAndRejectsConflicts(t *testing.T) {
	observedAt := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	observation := providerObservation(observedAt)
	var set storage.ProviderVersionObservationSet
	if err := set.Add(observation); err != nil {
		t.Fatal(err)
	}
	if err := set.Add(observation); err != nil {
		t.Fatalf("identical retry = %v", err)
	}
	if got := set.Snapshot(); len(got) != 1 || got[0] != observation {
		t.Fatalf("snapshot = %#v", got)
	}

	changedBytes := observation
	changedBytes.SHA256 = strings.Repeat("b", 64)
	if err := set.Add(changedBytes); !errors.Is(err, storage.ErrObservationConflict) {
		t.Fatalf("changed byte identity error = %v", err)
	}
	changedProfile := observation
	changedProfile.Profile.Bucket = "other-bucket"
	if err := set.Add(changedProfile); !errors.Is(err, storage.ErrObservationConflict) {
		t.Fatalf("changed profile identity error = %v", err)
	}
	if got := set.Snapshot(); len(got) != 1 || got[0] != observation {
		t.Fatalf("conflict replaced winner: %#v", got)
	}
}

func TestProviderVersionObservationSetConcurrentIdenticalRetries(t *testing.T) {
	observation := providerObservation(time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC))
	var set storage.ProviderVersionObservationSet
	start := make(chan struct{})
	errs := make(chan error, 32)
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			errs <- set.Add(observation)
		}()
	}
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent identical retry = %v", err)
		}
	}
	if got := set.Snapshot(); len(got) != 1 || got[0] != observation {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestProviderVersionObservationSetRejectsVersionReplacement(t *testing.T) {
	first := providerObservation(time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC))
	second := first
	second.VersionID = "provider-version-2"
	second.CapturedAt = first.CapturedAt.Add(time.Second)
	var set storage.ProviderVersionObservationSet
	if err := set.Add(first); err != nil {
		t.Fatal(err)
	}
	if err := set.Add(second); !errors.Is(err, storage.ErrObservationConflict) {
		t.Fatalf("version replacement error = %v", err)
	}
	got := set.Snapshot()
	if len(got) != 1 || got[0] != first {
		t.Fatalf("version conflict replaced winner = %#v", got)
	}
}

func TestProviderVersionObservationValidationFailsClosed(t *testing.T) {
	valid := providerObservation(time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC))
	tests := []struct {
		name   string
		mutate func(*storage.ProviderVersionObservation)
	}{
		{name: "missing version", mutate: func(o *storage.ProviderVersionObservation) { o.VersionID = "" }},
		{name: "latest version", mutate: func(o *storage.ProviderVersionObservation) { o.VersionID = "latest" }},
		{name: "wrong namespace", mutate: func(o *storage.ProviderVersionObservation) { o.ObjectKey = "other/key" }},
		{name: "invalid digest", mutate: func(o *storage.ProviderVersionObservation) { o.SHA256 = "bad" }},
		{name: "invalid size", mutate: func(o *storage.ProviderVersionObservation) { o.Size = -1 }},
		{name: "local timestamp", mutate: func(o *storage.ProviderVersionObservation) {
			o.CapturedAt = time.Date(2026, 9, 10, 8, 0, 0, 0, time.FixedZone("other", 3600))
		}},
		{name: "sub-microsecond timestamp", mutate: func(o *storage.ProviderVersionObservation) {
			o.CapturedAt = time.Date(2026, 9, 10, 8, 0, 0, 1, time.UTC)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := valid
			test.mutate(&observation)
			if err := storage.ValidateProviderVersionObservation(observation); !errors.Is(err, storage.ErrProviderVersion) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func providerObservation(capturedAt time.Time) storage.ProviderVersionObservation {
	return storage.ProviderVersionObservation{
		Profile: storage.ProviderProfileIdentity{
			ProfileID: "managed-source-profile", Implementation: "s3", AccountIdentity: "qualification",
			Endpoint: "https://s3.example.test", Region: "test-region-1", Bucket: "managed-data", Namespace: "project-a",
		},
		ObjectKey: "project-a/blobs/sha256/aa/" + strings.Repeat("a", 64), VersionID: "provider-version-1",
		SHA256: strings.Repeat("a", 64), Size: 10, CapturedAt: capturedAt,
	}
}
