package product

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"io"
	"sync"
	"testing"
	"time"
)

func TestServicePersistsIdentityLogoAndAtomicAudit(t *testing.T) {
	store := newMemoryStorage()
	blobs := &memoryBlobs{values: map[string][]byte{}}
	service, err := NewWithStorage(store, blobs)
	if err != nil {
		t.Fatal(err)
	}

	initial, err := service.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if initial.DisplayName != "LeapView" || initial.Revision != 1 || initial.Logo != nil {
		t.Fatalf("initial identity = %#v", initial)
	}

	updated, err := service.SetDisplayName(t.Context(), initial.Revision, "  Acme Analytics  ", Mutation{PrincipalID: "principal_admin", RequestID: "req_1"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != "Acme Analytics" || updated.Revision != 2 {
		t.Fatalf("updated identity = %#v", updated)
	}

	logoBytes := testPNG(t, 80, 40)
	withLogo, err := service.UploadLogo(t.Context(), updated.Revision, "image/png", bytes.NewReader(logoBytes), Mutation{PrincipalID: "principal_admin", RequestID: "req_2"})
	if err != nil {
		t.Fatal(err)
	}
	if withLogo.Logo == nil || withLogo.Logo.Width != 80 || withLogo.Logo.Height != 40 || withLogo.Revision != 3 {
		t.Fatalf("identity with logo = %#v", withLogo)
	}
	wantDigest := sha256.Sum256(logoBytes)
	if withLogo.Logo.SHA256 != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("logo digest = %q", withLogo.Logo.SHA256)
	}

	reader, logo, err := service.OpenLogo(t.Context(), withLogo.Logo.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(reader)
	_ = reader.Close()
	if !bytes.Equal(got, logoBytes) || logo != *withLogo.Logo {
		t.Fatalf("opened logo metadata=%#v bytes=%d", logo, len(got))
	}

	if got := store.auditCount(); got != 2 {
		t.Fatalf("product audit events = %d, want 2", got)
	}

	reset, err := service.ResetIdentity(t.Context(), withLogo.Revision, Mutation{PrincipalID: "principal_admin", RequestID: "req_3"})
	if err != nil {
		t.Fatal(err)
	}
	if reset.DisplayName != DefaultDisplayName || reset.Logo != nil || reset.Revision != 4 {
		t.Fatalf("reset identity = %#v", reset)
	}
	if got := store.lastAction(); got != "product.identity.reset" {
		t.Fatalf("reset audit action = %q", got)
	}
}

func TestServiceRejectsStaleRevisionAndInvalidLogoWithoutAudit(t *testing.T) {
	store := newMemoryStorage()
	blobs := &memoryBlobs{values: map[string][]byte{}}
	service, err := NewWithStorage(store, blobs)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.SetDisplayName(t.Context(), 99, "Stale", Mutation{}); err != ErrPrecondition {
		t.Fatalf("stale update error = %v", err)
	}
	if _, err := service.UploadLogo(t.Context(), 99, "image/png", bytes.NewReader(testPNG(t, 2, 2)), Mutation{}); err != ErrPrecondition {
		t.Fatalf("stale logo error = %v", err)
	}
	if len(blobs.values) != 0 {
		t.Fatalf("stale logo persisted %d blobs", len(blobs.values))
	}
	if _, err := service.UploadLogo(t.Context(), 1, "image/svg+xml", bytes.NewBufferString("<svg/>"), Mutation{}); err == nil {
		t.Fatal("SVG logo upload succeeded")
	}
	if _, err := service.UploadLogo(t.Context(), 1, "image/jpeg", bytes.NewReader(testPNG(t, 2, 2)), Mutation{}); err == nil {
		t.Fatal("mismatched logo Content-Type succeeded")
	}
	if got := store.auditCount(); got != 0 {
		t.Fatalf("failed mutations wrote %d audit events", got)
	}
}

func TestInspectLogoRejectsMalformedAndOversizedWebP(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "malformed payload",
			raw:  testWebPVP8XHeader(1, 1),
		},
		{
			name: "oversized dimension",
			raw:  testWebPVP8XHeader(MaxLogoPixels+1, 1),
		},
		{
			name: "oversized height",
			raw:  testWebPVP8XHeader(1, MaxLogoPixels+1),
		},
		{
			name: "oversized pixel count",
			raw:  testWebPVP8XHeader(4_000, 4_001),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logo, err := inspectLogo("image/webp", tt.raw)
			if err == nil {
				t.Fatal("inspectLogo accepted malformed or oversized image")
			}
			if logo != (Logo{}) {
				t.Fatalf("rejected logo metadata = %#v", logo)
			}
		})
	}
}

func TestServiceRejectsMalformedAndOversizedLogoWithoutPersistenceOrAudit(t *testing.T) {
	store := newMemoryStorage()
	blobs := &memoryBlobs{values: map[string][]byte{}}
	service, err := NewWithStorage(store, blobs)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "malformed payload",
			raw:  testWebPVP8XHeader(1, 1),
		},
		{
			name: "oversized dimension",
			raw:  testWebPVP8XHeader(MaxLogoPixels+1, 1),
		},
		{
			name: "oversized height",
			raw:  testWebPVP8XHeader(1, MaxLogoPixels+1),
		},
		{
			name: "oversized pixel count",
			raw:  testWebPVP8XHeader(4_000, 4_001),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.UploadLogo(t.Context(), 1, "image/webp", bytes.NewReader(tt.raw), Mutation{PrincipalID: "principal_admin", RequestID: tt.name})
			if err == nil {
				t.Fatal("UploadLogo accepted malformed or oversized image")
			}
			if len(blobs.values) != 0 {
				t.Fatalf("rejected logo persisted %d blobs", len(blobs.values))
			}
			identity, getErr := service.Get(t.Context())
			if getErr != nil {
				t.Fatal(getErr)
			}
			if identity.Revision != 1 || identity.Logo != nil {
				t.Fatalf("identity changed after rejected logo = %#v", identity)
			}
		})
	}

	if got := store.auditCount(); got != 0 {
		t.Fatalf("rejected logo mutations wrote %d audit events", got)
	}
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	value := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			value.Set(x, y, color.NRGBA{R: 20, G: 80, B: 140, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, value); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func testWebPVP8XHeader(width, height int) []byte {
	widthMinusOne := uint32(width - 1)
	heightMinusOne := uint32(height - 1)
	return []byte{
		'R', 'I', 'F', 'F', 22, 0, 0, 0,
		'W', 'E', 'B', 'P',
		'V', 'P', '8', 'X', 10, 0, 0, 0,
		0, 0, 0, 0,
		byte(widthMinusOne), byte(widthMinusOne >> 8), byte(widthMinusOne >> 16),
		byte(heightMinusOne), byte(heightMinusOne >> 8), byte(heightMinusOne >> 16),
	}
}

type memoryBlobs struct{ values map[string][]byte }

type memoryStorage struct {
	mu       sync.Mutex
	identity Identity
	audit    []MutationRequest
}

func newMemoryStorage() *memoryStorage {
	return &memoryStorage{identity: Identity{
		DisplayName: DefaultDisplayName,
		Revision:    1,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}}
}

func (s *memoryStorage) Get(context.Context) (Identity, error) {
	if s == nil {
		return Identity{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneIdentity(s.identity), nil
}

func (s *memoryStorage) Ping(context.Context) error {
	if s == nil {
		return ErrInvalid
	}
	return nil
}

func (s *memoryStorage) Mutate(ctx context.Context, req MutationRequest) (Identity, error) {
	if s == nil {
		return Identity{}, ErrInvalid
	}
	if req.ExpectedRevision <= 0 {
		return Identity{}, ErrPrecondition
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identity.Revision != req.ExpectedRevision {
		return Identity{}, ErrPrecondition
	}
	if req.CheckConcurrency != nil {
		if err := req.CheckConcurrency(ctx, s.identity.Revision); err != nil {
			return Identity{}, err
		}
	}
	switch req.Kind {
	case MutationDisplayName:
		s.identity.DisplayName = req.DisplayName
	case MutationLogo:
		if req.Logo == nil {
			return Identity{}, ErrInvalid
		}
		logo := *req.Logo
		s.identity.Logo = &logo
	case MutationDeleteLogo:
		if s.identity.Logo == nil {
			return Identity{}, ErrPrecondition
		}
		s.identity.Logo = nil
	case MutationReset:
		s.identity.DisplayName = DefaultDisplayName
		s.identity.Logo = nil
	default:
		return Identity{}, ErrInvalid
	}
	s.identity.Revision++
	s.identity.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.audit = append(s.audit, req)
	return cloneIdentity(s.identity), nil
}

func (s *memoryStorage) auditCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.audit)
}

func (s *memoryStorage) lastAction() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.audit) == 0 {
		return ""
	}
	return s.audit[len(s.audit)-1].Action
}

func cloneIdentity(identity Identity) Identity {
	if identity.Logo != nil {
		logo := *identity.Logo
		identity.Logo = &logo
	}
	return identity
}

func (s *memoryBlobs) Put(_ context.Context, expected Blob, body io.Reader) (Blob, error) {
	value, err := io.ReadAll(body)
	if err != nil {
		return Blob{}, err
	}
	s.values[expected.SHA256] = value
	return expected, nil
}

func (s *memoryBlobs) Open(_ context.Context, digest string) (io.ReadCloser, error) {
	value, ok := s.values[digest]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(value)), nil
}
