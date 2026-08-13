package avatar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"testing"
)

func TestServiceUploadNormalizesAndPersistsAvatar(t *testing.T) {
	repository := &memoryRepository{}
	blobs := newMemoryBlobStore()
	service, err := New(repository, blobs)
	if err != nil {
		t.Fatal(err)
	}

	input := image.NewNRGBA(image.Rect(0, 0, 640, 320))
	for y := 0; y < input.Bounds().Dy(); y++ {
		for x := 0; x < input.Bounds().Dx(); x++ {
			input.Set(x, y, color.NRGBA{R: uint8(x % 255), G: uint8(y % 255), B: 80, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, input, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}

	metadata, err := service.Upload(t.Context(), "principal_1", bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.PrincipalID != "principal_1" || metadata.MediaType != "image/png" || metadata.Width != 256 || metadata.Height != 256 {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.SizeBytes <= 0 || len(metadata.SHA256) != 64 || repository.value.SHA256 != metadata.SHA256 {
		t.Fatalf("metadata persistence = %#v repository=%#v", metadata, repository.value)
	}

	reader, stored, err := service.Open(t.Context(), "principal_1", metadata.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	decoded, format, err := image.Decode(reader)
	if err != nil {
		t.Fatal(err)
	}
	if format != "png" || decoded.Bounds() != image.Rect(0, 0, 256, 256) || stored != metadata {
		t.Fatalf("stored avatar format=%q bounds=%v metadata=%#v", format, decoded.Bounds(), stored)
	}
}

func TestServiceRejectsUnsupportedAndOversizedImages(t *testing.T) {
	service, err := New(&memoryRepository{}, newMemoryBlobStore())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Upload(t.Context(), "principal_1", bytes.NewBufferString(`<svg xmlns="http://www.w3.org/2000/svg"/>`)); err == nil {
		t.Fatal("SVG upload succeeded")
	}
	if _, err := service.Upload(t.Context(), "principal_1", bytes.NewReader(make([]byte, MaxUploadBytes+1))); err == nil {
		t.Fatal("oversized upload succeeded")
	}

	large := image.NewNRGBA(image.Rect(0, 0, 5000, 4000))
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, large); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Upload(t.Context(), "principal_1", bytes.NewReader(encoded.Bytes())); err == nil {
		t.Fatal("oversized dimensions succeeded")
	}
}

func TestServiceDeleteRemovesMetadataAndMakesOldDigestUnavailable(t *testing.T) {
	repository := &memoryRepository{value: Metadata{PrincipalID: "principal_1", SHA256: string(make([]byte, 64)), MediaType: "image/png", SizeBytes: 1, Width: 256, Height: 256}}
	service, err := New(repository, newMemoryBlobStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(t.Context(), "principal_1"); err != nil {
		t.Fatal(err)
	}
	if repository.value.PrincipalID != "" {
		t.Fatalf("metadata remains: %#v", repository.value)
	}
	if _, _, err := service.Open(t.Context(), "principal_1", string(make([]byte, 64))); err == nil {
		t.Fatal("deleted avatar remained readable")
	}
}

func TestJPEGEXIFOrientationIsAppliedBeforeCropping(t *testing.T) {
	exif := []byte{
		0xff, 0xd8, 0xff, 0xe1, 0x00, 0x22,
		'E', 'x', 'i', 'f', 0, 0,
		'I', 'I', 0x2a, 0x00, 0x08, 0x00, 0x00, 0x00,
		0x01, 0x00,
		0x12, 0x01, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00, 0x06, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0xff, 0xd9,
	}
	if got := jpegEXIFOrientation(exif); got != 6 {
		t.Fatalf("jpegEXIFOrientation() = %d, want 6", got)
	}
	source := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	source.Set(0, 0, color.NRGBA{R: 255, A: 255})
	rotated := orient(source, 6)
	if rotated.Bounds() != image.Rect(0, 0, 3, 2) || color.NRGBAModel.Convert(rotated.At(2, 0)).(color.NRGBA).R != 255 {
		t.Fatalf("rotated bounds=%v top-right=%v", rotated.Bounds(), rotated.At(2, 0))
	}
}

type memoryRepository struct{ value Metadata }

func (r *memoryRepository) Avatar(context.Context, string) (Metadata, error) {
	if r.value.PrincipalID == "" {
		return Metadata{}, ErrNotFound
	}
	return r.value, nil
}

func (r *memoryRepository) UpsertAvatar(_ context.Context, value Metadata) (Metadata, error) {
	r.value = value
	return value, nil
}

func (r *memoryRepository) DeleteAvatar(context.Context, string) error {
	if r.value.PrincipalID == "" {
		return ErrNotFound
	}
	r.value = Metadata{}
	return nil
}

type memoryBlobStore struct{ values map[string][]byte }

func newMemoryBlobStore() *memoryBlobStore { return &memoryBlobStore{values: map[string][]byte{}} }

func (s *memoryBlobStore) Put(_ context.Context, expected Blob, body io.Reader) (Blob, error) {
	value, err := io.ReadAll(body)
	if err != nil {
		return Blob{}, err
	}
	digest := sha256.Sum256(value)
	if hex.EncodeToString(digest[:]) != expected.SHA256 || int64(len(value)) != expected.Size {
		return Blob{}, errors.New("integrity mismatch")
	}
	s.values[expected.SHA256] = value
	return expected, nil
}

func (s *memoryBlobStore) Open(_ context.Context, digest string) (io.ReadCloser, error) {
	value, ok := s.values[digest]
	if !ok {
		return nil, ErrBlobNotFound
	}
	return io.NopCloser(bytes.NewReader(value)), nil
}
