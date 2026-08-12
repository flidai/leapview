// Package avatar validates, normalizes, stores, and resolves user profile images.
package avatar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	MaxUploadBytes int64 = 5 << 20
	MaxPixels            = 16_000_000
	OutputSize           = 256
)

var (
	ErrInvalid      = errors.New("invalid avatar")
	ErrBlobNotFound = errors.New("avatar blob not found")
	ErrNotFound     = errors.New("avatar not found")
	ErrTooLarge     = errors.New("avatar is too large")
)

type Metadata struct {
	PrincipalID string
	SHA256      string
	MediaType   string
	SizeBytes   int64
	Width       int
	Height      int
	UpdatedAt   string
}

type Repository interface {
	Avatar(context.Context, string) (Metadata, error)
	UpsertAvatar(context.Context, Metadata) (Metadata, error)
	DeleteAvatar(context.Context, string) error
}

type BlobStore interface {
	Put(context.Context, Blob, io.Reader) (Blob, error)
	Open(context.Context, string) (io.ReadCloser, error)
}

type Blob struct {
	SHA256 string
	Size   int64
}

type Service struct {
	repository Repository
	blobs      BlobStore
}

func New(repository Repository, blobs BlobStore) (*Service, error) {
	if repository == nil {
		return nil, fmt.Errorf("avatar repository is required")
	}
	if blobs == nil {
		return nil, fmt.Errorf("avatar blob store is required")
	}
	return &Service{repository: repository, blobs: blobs}, nil
}

func (s *Service) Upload(ctx context.Context, principalID string, body io.Reader) (Metadata, error) {
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return Metadata{}, fmt.Errorf("%w: principal id is required", ErrInvalid)
	}
	if body == nil {
		return Metadata{}, fmt.Errorf("%w: image body is required", ErrInvalid)
	}
	raw, err := io.ReadAll(io.LimitReader(body, MaxUploadBytes+1))
	if err != nil {
		return Metadata{}, fmt.Errorf("read avatar: %w", err)
	}
	if int64(len(raw)) > MaxUploadBytes {
		return Metadata{}, fmt.Errorf("%w: image exceeds %d bytes", ErrTooLarge, MaxUploadBytes)
	}
	normalized, err := normalize(raw)
	if err != nil {
		return Metadata{}, err
	}
	digest := sha256.Sum256(normalized)
	sha := hex.EncodeToString(digest[:])
	blob := Blob{SHA256: sha, Size: int64(len(normalized))}
	if _, err := s.blobs.Put(ctx, blob, bytes.NewReader(normalized)); err != nil {
		return Metadata{}, fmt.Errorf("store avatar: %w", err)
	}
	metadata := Metadata{
		PrincipalID: principalID,
		SHA256:      sha,
		MediaType:   "image/png",
		SizeBytes:   int64(len(normalized)),
		Width:       OutputSize,
		Height:      OutputSize,
	}
	stored, err := s.repository.UpsertAvatar(ctx, metadata)
	if err != nil {
		return Metadata{}, fmt.Errorf("persist avatar metadata: %w", err)
	}
	return stored, nil
}

// Current returns the principal's current avatar metadata without opening the
// immutable image blob. It is used to embed a content-addressed avatar URL in
// profile responses.
func (s *Service) Current(ctx context.Context, principalID string) (Metadata, error) {
	return s.repository.Avatar(ctx, strings.TrimSpace(principalID))
}

func (s *Service) Open(ctx context.Context, principalID, digest string) (io.ReadCloser, Metadata, error) {
	metadata, err := s.repository.Avatar(ctx, strings.TrimSpace(principalID))
	if err != nil {
		return nil, Metadata{}, err
	}
	if metadata.SHA256 == "" || metadata.SHA256 != strings.ToLower(strings.TrimSpace(digest)) {
		return nil, Metadata{}, ErrNotFound
	}
	reader, err := s.blobs.Open(ctx, metadata.SHA256)
	if errors.Is(err, ErrBlobNotFound) {
		return nil, Metadata{}, ErrNotFound
	}
	if err != nil {
		return nil, Metadata{}, fmt.Errorf("open avatar blob: %w", err)
	}
	return reader, metadata, nil
}

func (s *Service) Delete(ctx context.Context, principalID string) error {
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return fmt.Errorf("%w: principal id is required", ErrInvalid)
	}
	return s.repository.DeleteAvatar(ctx, principalID)
}

func normalize(raw []byte) ([]byte, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: image could not be decoded", ErrInvalid)
	}
	if format != "jpeg" && format != "png" && format != "webp" {
		return nil, fmt.Errorf("%w: supported formats are JPEG, PNG, and WebP", ErrInvalid)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > MaxPixels/config.Height {
		return nil, fmt.Errorf("%w: image exceeds %d pixels", ErrInvalid, MaxPixels)
	}
	source, decodedFormat, err := image.Decode(bytes.NewReader(raw))
	if err != nil || decodedFormat != format {
		return nil, fmt.Errorf("%w: image could not be decoded", ErrInvalid)
	}
	if format == "jpeg" {
		source = orient(source, jpegEXIFOrientation(raw))
	}
	bounds := source.Bounds()
	side := bounds.Dx()
	if bounds.Dy() < side {
		side = bounds.Dy()
	}
	left := bounds.Min.X + (bounds.Dx()-side)/2
	top := bounds.Min.Y + (bounds.Dy()-side)/2
	crop := image.Rect(left, top, left+side, top+side)
	destination := image.NewNRGBA(image.Rect(0, 0, OutputSize, OutputSize))
	draw.CatmullRom.Scale(destination, destination.Bounds(), source, crop, draw.Over, nil)
	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := encoder.Encode(&output, destination); err != nil {
		return nil, fmt.Errorf("normalize avatar: %w", err)
	}
	return output.Bytes(), nil
}

func jpegEXIFOrientation(raw []byte) int {
	if len(raw) < 4 || raw[0] != 0xff || raw[1] != 0xd8 {
		return 1
	}
	for offset := 2; offset+4 <= len(raw); {
		if raw[offset] != 0xff {
			return 1
		}
		for offset < len(raw) && raw[offset] == 0xff {
			offset++
		}
		if offset >= len(raw) {
			return 1
		}
		marker := raw[offset]
		offset++
		if marker == 0xda || marker == 0xd9 {
			return 1
		}
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		if offset+2 > len(raw) {
			return 1
		}
		length := int(binary.BigEndian.Uint16(raw[offset : offset+2]))
		if length < 2 || offset+length > len(raw) {
			return 1
		}
		segment := raw[offset+2 : offset+length]
		if marker == 0xe1 && len(segment) >= 6 && bytes.Equal(segment[:6], []byte("Exif\x00\x00")) {
			if orientation := tiffOrientation(segment[6:]); orientation >= 1 && orientation <= 8 {
				return orientation
			}
		}
		offset += length
	}
	return 1
}

func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return 1
	}
	ifdOffset := uint64(order.Uint32(tiff[4:8]))
	if ifdOffset+2 > uint64(len(tiff)) {
		return 1
	}
	count := uint64(order.Uint16(tiff[ifdOffset : ifdOffset+2]))
	entries := ifdOffset + 2
	if count > (uint64(len(tiff))-entries)/12 {
		return 1
	}
	for index := uint64(0); index < count; index++ {
		entry := tiff[entries+index*12 : entries+(index+1)*12]
		if order.Uint16(entry[:2]) != 0x0112 || order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) != 1 {
			continue
		}
		return int(order.Uint16(entry[8:10]))
	}
	return 1
}

func orient(source image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return source
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	outputWidth, outputHeight := width, height
	if orientation >= 5 {
		outputWidth, outputHeight = height, width
	}
	destination := image.NewNRGBA(image.Rect(0, 0, outputWidth, outputHeight))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			destinationX, destinationY := orientedPoint(x, y, width, height, orientation)
			destination.Set(destinationX, destinationY, source.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return destination
}

func orientedPoint(x, y, width, height, orientation int) (int, int) {
	switch orientation {
	case 2:
		return width - 1 - x, y
	case 3:
		return width - 1 - x, height - 1 - y
	case 4:
		return x, height - 1 - y
	case 5:
		return y, x
	case 6:
		return height - 1 - y, x
	case 7:
		return height - 1 - y, width - 1 - x
	case 8:
		return y, width - 1 - x
	default:
		return x, y
	}
}
