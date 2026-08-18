package catalogseal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
)

type memoryCatalog struct {
	mu       sync.Mutex
	data     []byte
	openCall int
	failOpen map[int]error
}

func (c *memoryCatalog) Open(_ context.Context) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.openCall++
	if err := c.failOpen[c.openCall]; err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), c.data...))), nil
}

type memoryObject struct {
	bytes    []byte
	metadata ObjectMetadata
}

type memoryStore struct {
	mu         sync.Mutex
	objects    map[string]memoryObject
	createErr  error
	createCall int
	openCall   int
	corrupt    bool
}

func (s *memoryStore) Create(_ context.Context, key string, body io.Reader, metadata ObjectMetadata) error {
	s.mu.Lock()
	s.createCall++
	err := s.createErr
	if s.objects == nil {
		s.objects = make(map[string]memoryObject)
	}
	if _, exists := s.objects[key]; !exists && err == nil {
		data, readErr := io.ReadAll(body)
		if readErr != nil {
			s.mu.Unlock()
			return readErr
		}
		s.objects[key] = memoryObject{bytes: data, metadata: cloneMetadata(metadata)}
	}
	s.mu.Unlock()
	return err
}

func (s *memoryStore) Open(_ context.Context, key string) (Object, error) {
	s.mu.Lock()
	s.openCall++
	value, ok := s.objects[key]
	corrupt := s.corrupt
	s.mu.Unlock()
	if !ok {
		return Object{}, errors.New("not found")
	}
	data := append([]byte(nil), value.bytes...)
	metadata := cloneMetadata(value.metadata)
	if corrupt {
		data = append(data, 0)
	}
	return Object{Body: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data)), Metadata: metadata}, nil
}

type memoryRepository struct {
	mu             sync.Mutex
	record         SealRecord
	prepareCalls   int
	uploadCalls    int
	completeCalls  int
	prepareErr     error
	uploadErr      error
	completeErr    error
	prepareBefore  bool
	uploadBefore   bool
	completeBefore bool
}

func (r *memoryRepository) Lookup(_ context.Context, id string) (SealRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record.Identity.SealID == "" || r.record.Identity.SealID != id {
		return SealRecord{}, ErrSealNotFound
	}
	return r.record, nil
}

func (r *memoryRepository) Prepare(_ context.Context, identity SealIdentity) (SealRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prepareCalls++
	if r.prepareErr != nil {
		return SealRecord{}, r.prepareErr
	}
	if r.record.Identity.SealID == "" {
		r.record = SealRecord{Identity: identity, Status: SealPreparing}
		r.prepareBefore = true
	}
	if !sameIdentity(r.record.Identity, identity) {
		return SealRecord{}, ErrIdentityConflict
	}
	return r.record, nil
}

func (r *memoryRepository) MarkUploaded(_ context.Context, id string) (SealRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.uploadCalls++
	if r.uploadErr != nil {
		return SealRecord{}, r.uploadErr
	}
	if r.record.Identity.SealID != id {
		return SealRecord{}, ErrIdentityConflict
	}
	r.record.Status = SealUploaded
	r.uploadBefore = true
	return r.record, nil
}

func (r *memoryRepository) CompleteVerified(_ context.Context, input CompleteInput) (Completion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completeCalls++
	if r.completeErr != nil {
		return Completion{}, r.completeErr
	}
	if input.SealID != r.record.Identity.SealID || input.CandidateID != r.record.Identity.Candidate.ID {
		return Completion{}, ErrIdentityConflict
	}
	r.record.Status = SealVerified
	r.completeBefore = true
	return Completion{Seal: r.record, CandidateID: input.CandidateID, LeaseReleased: true}, nil
}

type memoryVerifier struct {
	store    *memoryStore
	verified int
	err      error
}

func (v *memoryVerifier) Verify(ctx context.Context, input RemoteVerification) error {
	if v.err != nil {
		return v.err
	}
	object, err := input.Open(ctx)
	if err != nil {
		return err
	}
	if !verifyObject(object, input.Identity) {
		return errors.New("object mismatch")
	}
	v.verified++
	return nil
}

func cloneMetadata(value ObjectMetadata) ObjectMetadata {
	copyValue := make(ObjectMetadata, len(value))
	for key, item := range value {
		copyValue[key] = item
	}
	return copyValue
}

func testRequest(catalog DetachedCatalog, store *memoryStore, repository *memoryRepository, verifier RemoteVerifier) Request {
	return Request{
		SealID:        "seal-1",
		Attempt:       AttemptIdentity{ID: "attempt-1", WriterLeaseID: "writer-1"},
		Plan:          PlanIdentity{ID: "plan-1", Digest: testDigest('1'), ExecutionDigest: testDigest('2')},
		Pool:          PoolIdentity{ID: "pool-1", CompatibilityDigest: testDigest('3')},
		Qualification: QualificationIdentity{Digest: testDigest('4')},
		Closure:       ClosureIdentity{Digest: testDigest('5')},
		Candidate:     CandidateIdentity{ID: "candidate-1", ServingArtifactID: "artifact-1", ServingArtifactDigest: testDigest('6'), ServingStateID: "state-1"},
		Catalog:       catalog, Store: store, Repository: repository, Verifier: verifier,
	}
}

func testDigest(char byte) string {
	return "sha256:" + string(bytes.Repeat([]byte{char}, 64))
}

func catalogDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestSealCreateOnlyAndAtomicCompletion(t *testing.T) {
	catalog := &memoryCatalog{data: []byte("catalog bytes"), failOpen: map[int]error{}}
	store := &memoryStore{objects: make(map[string]memoryObject)}
	repository := &memoryRepository{}
	verifier := &memoryVerifier{}
	completion, err := Seal(context.Background(), testRequest(catalog, store, repository, verifier))
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if completion.CandidateID != "candidate-1" || !completion.LeaseReleased {
		t.Fatalf("completion = %#v", completion)
	}
	if !repository.prepareBefore || !repository.uploadBefore || !repository.completeBefore {
		t.Fatalf("repository boundary order was not preserved: %#v", repository)
	}
	if store.createCall != 1 || verifier.verified != 1 {
		t.Fatalf("create=%d verify=%d, want one each", store.createCall, verifier.verified)
	}
	wantKey := CanonicalObjectKey(catalogDigest(catalog.data))
	if _, ok := store.objects[wantKey]; !ok {
		t.Fatalf("content-addressed object key %q was not created", wantKey)
	}
}

func TestSealLostCreateAcknowledgementReconcilesMatchingObject(t *testing.T) {
	catalog := &memoryCatalog{data: []byte("catalog bytes"), failOpen: map[int]error{}}
	store := &memoryStore{objects: make(map[string]memoryObject), createErr: errors.New("ambiguous acknowledgement")}
	dataDigest := catalogDigest(catalog.data)
	key := CanonicalObjectKey(dataDigest)
	store.objects[key] = memoryObject{bytes: append([]byte(nil), catalog.data...), metadata: ObjectMetadata{MetadataDigest: dataDigest, MetadataSize: "13"}}
	repository := &memoryRepository{}
	verifier := &memoryVerifier{}
	if _, err := Seal(context.Background(), testRequest(catalog, store, repository, verifier)); err != nil {
		t.Fatalf("lost acknowledgement should converge: %v", err)
	}
	if store.createCall != 1 || store.openCall < 2 {
		t.Fatalf("create=%d open=%d, expected create and reconciliation reads", store.createCall, store.openCall)
	}
}

func TestSealRecoversRemoteCreateBeforeMarkWithLostLocal(t *testing.T) {
	catalog := &memoryCatalog{data: []byte("catalog bytes"), failOpen: map[int]error{}}
	store := &memoryStore{objects: make(map[string]memoryObject)}
	repository := &memoryRepository{}
	request := testRequest(catalog, store, repository, &memoryVerifier{})
	// Establish only the durable preparing identity, then model a successful
	// object create whose acknowledgement and mark transition were lost.
	identity, err := localIdentity(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Prepare(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	store.objects[identity.ObjectKey] = memoryObject{bytes: append([]byte(nil), catalog.data...), metadata: ObjectMetadata{MetadataDigest: identity.CatalogDigest, MetadataSize: "13"}}
	request.Catalog = nil
	if _, err := Seal(context.Background(), request); err != nil {
		t.Fatalf("recovery after remote create = %v", err)
	}
	if repository.uploadCalls != 1 || store.createCall != 0 {
		t.Fatalf("recovery rewrote object or skipped mark: create=%d mark=%d", store.createCall, repository.uploadCalls)
	}
}

func TestSealRejectsCorruptPreexistingObject(t *testing.T) {
	catalog := &memoryCatalog{data: []byte("catalog bytes"), failOpen: map[int]error{}}
	store := &memoryStore{objects: make(map[string]memoryObject), createErr: errors.New("already exists")}
	dataDigest := catalogDigest(catalog.data)
	store.objects[CanonicalObjectKey(dataDigest)] = memoryObject{bytes: []byte("different"), metadata: ObjectMetadata{MetadataDigest: dataDigest, MetadataSize: "13"}}
	repository := &memoryRepository{}
	_, err := Seal(context.Background(), testRequest(catalog, store, repository, &memoryVerifier{}))
	if !errors.Is(err, ErrObjectCorrupt) {
		t.Fatalf("error = %v, want ErrObjectCorrupt", err)
	}
	if repository.uploadCalls != 0 {
		t.Fatalf("mark uploaded was called after corrupt object")
	}
}

func TestSealLocalLossBeforeUploadDoesNotSubstituteRemoteObject(t *testing.T) {
	catalog := &memoryCatalog{data: []byte("catalog bytes"), failOpen: map[int]error{2: errors.New("local removed")}}
	store := &memoryStore{objects: make(map[string]memoryObject)}
	repository := &memoryRepository{}
	_, err := Seal(context.Background(), testRequest(catalog, store, repository, &memoryVerifier{}))
	if !errors.Is(err, ErrLocalCatalog) {
		t.Fatalf("error = %v, want ErrLocalCatalog", err)
	}
	if store.createCall != 0 || repository.prepareCalls != 1 {
		t.Fatalf("local loss caused substitution or durable identity: create=%d prepare=%d", store.createCall, repository.prepareCalls)
	}
}

func TestSealRetryAfterUploadedStateConverges(t *testing.T) {
	catalog := &memoryCatalog{data: []byte("catalog bytes"), failOpen: map[int]error{}}
	store := &memoryStore{objects: make(map[string]memoryObject)}
	repository := &memoryRepository{}
	verifier := &memoryVerifier{}
	request := testRequest(catalog, store, repository, verifier)
	if _, err := Seal(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := Seal(context.Background(), request); err != nil {
		t.Fatalf("retry error = %v", err)
	}
	if store.createCall != 1 {
		t.Fatalf("create called %d times, want one", store.createCall)
	}
	if verifier.verified != 1 {
		t.Fatalf("verified %d times, want one after durable completion", verifier.verified)
	}
}

func TestSealRecoversAfterMarkUploadedWithLostLocal(t *testing.T) {
	catalog := &memoryCatalog{data: []byte("catalog bytes"), failOpen: map[int]error{}}
	store := &memoryStore{objects: make(map[string]memoryObject)}
	repository := &memoryRepository{}
	verifier := &memoryVerifier{err: errors.New("crash after mark")}
	request := testRequest(catalog, store, repository, verifier)
	if _, err := Seal(context.Background(), request); !errors.Is(err, ErrRemoteVerification) {
		t.Fatalf("first attempt error = %v", err)
	}
	if repository.record.Status != SealUploaded {
		t.Fatalf("status after verifier crash = %s", repository.record.Status)
	}
	verifier.err = nil
	request.Catalog = nil
	if _, err := Seal(context.Background(), request); err != nil {
		t.Fatalf("uploaded recovery = %v", err)
	}
}

func TestSealRecomputeAfterLostLocalLeavesUnreferencedObjectForGC(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{objects: make(map[string]memoryObject)}
	firstCatalog := &memoryCatalog{data: []byte("first attempt catalog"), failOpen: map[int]error{}}
	firstRepository := &memoryRepository{}
	firstVerifier := &memoryVerifier{err: errors.New("lost remote verification acknowledgement")}
	firstRequest := testRequest(firstCatalog, store, firstRepository, firstVerifier)
	if _, err := Seal(ctx, firstRequest); !errors.Is(err, ErrRemoteVerification) {
		t.Fatalf("first attempt error = %v, want remote verification failure", err)
	}
	firstIdentity, err := localIdentity(ctx, firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	if firstRepository.record.Status != SealUploaded {
		t.Fatalf("first attempt status = %s, want uploaded", firstRepository.record.Status)
	}
	// A retry after local staging loss can inspect the exact uploaded object,
	// but it cannot claim completion while remote verification is indeterminate.
	firstRequest.Catalog = nil
	if _, err := Seal(ctx, firstRequest); !errors.Is(err, ErrRemoteVerification) {
		t.Fatalf("lost-local retry error = %v, want remote verification failure", err)
	}

	// Recompute under a fresh attempt/seal identity. The old content-addressed
	// object remains unreferenced and is left for the fenced GC collector.
	secondCatalog := &memoryCatalog{data: []byte("recomputed catalog"), failOpen: map[int]error{}}
	secondRepository := &memoryRepository{}
	secondRequest := testRequest(secondCatalog, store, secondRepository, &memoryVerifier{})
	secondRequest.SealID = "seal-recomputed"
	secondRequest.Attempt.ID = "attempt-recomputed"
	secondRequest.Attempt.WriterLeaseID = "writer-recomputed"
	secondRequest.Candidate.ID = "candidate-recomputed"
	if _, err := Seal(ctx, secondRequest); err != nil {
		t.Fatalf("recomputed attempt error = %v", err)
	}
	secondIdentity, err := localIdentity(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if firstIdentity.ObjectKey == secondIdentity.ObjectKey {
		t.Fatal("recomputed attempt reused the lost attempt's object identity")
	}
	if _, ok := store.objects[firstIdentity.ObjectKey]; !ok {
		t.Fatal("unreferenced first-attempt object was not retained for GC")
	}
	if _, ok := store.objects[secondIdentity.ObjectKey]; !ok {
		t.Fatal("recomputed sealed object was not created")
	}
}

func TestSealRecoversAfterVerificationBeforeCompletionWithLostLocal(t *testing.T) {
	catalog := &memoryCatalog{data: []byte("catalog bytes"), failOpen: map[int]error{}}
	store := &memoryStore{objects: make(map[string]memoryObject)}
	repository := &memoryRepository{completeErr: errors.New("crash after verification")}
	verifier := &memoryVerifier{}
	request := testRequest(catalog, store, repository, verifier)
	if _, err := Seal(context.Background(), request); !errors.Is(err, ErrSealRepository) {
		t.Fatalf("first attempt error = %v", err)
	}
	if repository.record.Status != SealUploaded {
		t.Fatalf("status after completion crash = %s", repository.record.Status)
	}
	repository.completeErr = nil
	request.Catalog = nil
	if _, err := Seal(context.Background(), request); err != nil {
		t.Fatalf("completion recovery = %v", err)
	}
}

func TestSealRejectsCorruptUploadedRecoveryWithLostLocal(t *testing.T) {
	catalog := &memoryCatalog{data: []byte("catalog bytes"), failOpen: map[int]error{}}
	store := &memoryStore{objects: make(map[string]memoryObject)}
	repository := &memoryRepository{}
	verifier := &memoryVerifier{err: errors.New("crash after mark")}
	request := testRequest(catalog, store, repository, verifier)
	if _, err := Seal(context.Background(), request); !errors.Is(err, ErrRemoteVerification) {
		t.Fatalf("first attempt error = %v", err)
	}
	store.objects[CanonicalObjectKey(catalogDigest(catalog.data))] = memoryObject{bytes: []byte("corrupt"), metadata: ObjectMetadata{MetadataDigest: catalogDigest(catalog.data), MetadataSize: "13"}}
	request.Catalog = nil
	if _, err := Seal(context.Background(), request); !errors.Is(err, ErrObjectCorrupt) {
		t.Fatalf("corrupt recovery error = %v", err)
	}
}

func TestSealFaultInjectionBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*memoryCatalog, *memoryStore, *memoryRepository, *memoryVerifier)
		want  error
	}{
		{name: "hash local open", setup: func(c *memoryCatalog, _ *memoryStore, _ *memoryRepository, _ *memoryVerifier) {
			c.failOpen[1] = errors.New("missing")
		}, want: ErrLocalCatalog},
		{name: "prepare", setup: func(_ *memoryCatalog, _ *memoryStore, r *memoryRepository, _ *memoryVerifier) {
			r.prepareErr = errors.New("database unavailable")
		}, want: ErrSealRepository},
		{name: "create no object", setup: func(_ *memoryCatalog, s *memoryStore, _ *memoryRepository, _ *memoryVerifier) {
			s.createErr = errors.New("timeout")
		}, want: ErrObjectUpload},
		{name: "mark uploaded", setup: func(_ *memoryCatalog, _ *memoryStore, r *memoryRepository, _ *memoryVerifier) {
			r.uploadErr = errors.New("database unavailable")
		}, want: ErrSealRepository},
		{name: "remote verify", setup: func(_ *memoryCatalog, _ *memoryStore, _ *memoryRepository, v *memoryVerifier) {
			v.err = errors.New("snapshot mismatch")
		}, want: ErrRemoteVerification},
		{name: "complete", setup: func(_ *memoryCatalog, _ *memoryStore, r *memoryRepository, _ *memoryVerifier) {
			r.completeErr = errors.New("database unavailable")
		}, want: ErrSealRepository},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := &memoryCatalog{data: []byte("catalog bytes"), failOpen: map[int]error{}}
			store := &memoryStore{objects: make(map[string]memoryObject)}
			repository := &memoryRepository{}
			verifier := &memoryVerifier{}
			test.setup(catalog, store, repository, verifier)
			_, err := Seal(context.Background(), testRequest(catalog, store, repository, verifier))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCanonicalObjectKeyRejectsInvalidDigest(t *testing.T) {
	if got := CanonicalObjectKey("not-a-digest"); got != "" {
		t.Fatalf("invalid digest key = %q", got)
	}
	if !validObjectKey(CanonicalObjectKey(testDigest('a'))) {
		t.Fatal("canonical key did not satisfy object-key rules")
	}
}

var _ = fmt.Sprintf
