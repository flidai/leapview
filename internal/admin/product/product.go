// Package product owns mutable instance identity and safe, read-only product
// administration status. Deployment configuration and secrets are never
// persisted through this package.
package product

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	apigenaudit "github.com/Yacobolo/toolbelt/apigen/runtime/audit"
	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	_ "golang.org/x/image/webp"
)

const (
	DefaultDisplayName       = "LeapView"
	MaxLogoBytes       int64 = 5 << 20
	MaxLogoPixels            = 16_000_000
)

var (
	ErrInvalid      = apigenfailure.New("invalid", "invalid product identity")
	ErrNotFound     = apigenfailure.New("not_found", "product logo not found")
	ErrPrecondition = apigenfailure.New("precondition", "product identity precondition failed")
)

type Logo struct {
	SHA256    string `json:"sha256"`
	MediaType string `json:"mediaType"`
	SizeBytes int64  `json:"sizeBytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

type Identity struct {
	DisplayName string `json:"displayName"`
	Logo        *Logo  `json:"logo,omitempty"`
	Revision    int64  `json:"revision"`
	UpdatedAt   string `json:"updatedAt"`
}

type Mutation struct {
	PrincipalID      string
	RequestID        string
	CorrelationID    string
	ConcurrencyToken string
}

type Blob struct {
	SHA256 string
	Size   int64
}

type BlobStore interface {
	Put(context.Context, Blob, io.Reader) (Blob, error)
	Open(context.Context, string) (io.ReadCloser, error)
}

type CommandExecutor interface {
	Execute(context.Context, string, apigencommand.Execution) error
	CheckConcurrency(context.Context, string, string, string) error
}

type Service struct {
	storage  Storage
	blobs    BlobStore
	commands CommandExecutor
}

// NewLegacySQLite is the explicit compatibility constructor for the
// historical embedded SQLite product store. Production composition should use
// NewWithStorage with the native PostgreSQL repository.
func NewLegacySQLite(db *sql.DB, blobs BlobStore) (*Service, error) {
	if db == nil {
		return nil, fmt.Errorf("product identity database is required")
	}
	if blobs == nil {
		return nil, fmt.Errorf("product logo blob store is required")
	}
	return &Service{storage: newSQLiteStorage(db), blobs: blobs}, nil
}

func NewWithStorage(storage Storage, blobs BlobStore) (*Service, error) {
	if storage == nil {
		return nil, fmt.Errorf("product identity storage is required")
	}
	if blobs == nil {
		return nil, fmt.Errorf("product logo blob store is required")
	}
	return &Service{storage: storage, blobs: blobs}, nil
}

func (s *Service) ConfigureCommandExecutor(executor CommandExecutor) {
	if s != nil {
		s.commands = executor
	}
}

func (s *Service) Get(ctx context.Context) (Identity, error) {
	if s == nil || s.storage == nil {
		return Identity{}, ErrInvalid
	}
	return s.storage.Get(ctx)
}

func (s *Service) Ping(ctx context.Context) error {
	if s == nil || s.storage == nil {
		return ErrInvalid
	}
	return s.storage.Ping(ctx)
}

func (s *Service) SetDisplayName(ctx context.Context, expectedRevision int64, displayName string, mutation Mutation) (Identity, error) {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || !utf8.ValidString(displayName) || utf8.RuneCountInString(displayName) > 120 {
		return Identity{}, fmt.Errorf("%w: displayName must contain 1 to 120 characters", ErrInvalid)
	}
	return s.mutate(ctx, expectedRevision, mutation, "product.identity.updated", map[string]any{"fields": []string{"displayName"}}, MutationRequest{Kind: MutationDisplayName, DisplayName: displayName})
}

func (s *Service) UploadLogo(ctx context.Context, expectedRevision int64, contentType string, body io.Reader, mutation Mutation) (Identity, error) {
	current, err := s.Get(ctx)
	if err != nil {
		return Identity{}, err
	}
	if expectedRevision <= 0 || current.Revision != expectedRevision {
		return Identity{}, ErrPrecondition
	}
	if body == nil {
		return Identity{}, fmt.Errorf("%w: logo body is required", ErrInvalid)
	}
	raw, err := io.ReadAll(io.LimitReader(body, MaxLogoBytes+1))
	if err != nil {
		return Identity{}, fmt.Errorf("read product logo: %w", err)
	}
	if int64(len(raw)) > MaxLogoBytes {
		return Identity{}, fmt.Errorf("%w: logo exceeds %d bytes", ErrInvalid, MaxLogoBytes)
	}
	logo, err := inspectLogo(contentType, raw)
	if err != nil {
		return Identity{}, err
	}
	if _, err := s.blobs.Put(ctx, Blob{SHA256: logo.SHA256, Size: logo.SizeBytes}, bytes.NewReader(raw)); err != nil {
		return Identity{}, fmt.Errorf("store product logo: %w", err)
	}
	return s.mutate(ctx, expectedRevision, mutation, "product.logo.updated", map[string]any{"sha256": logo.SHA256}, MutationRequest{Kind: MutationLogo, Logo: &logo})
}

func (s *Service) DeleteLogo(ctx context.Context, expectedRevision int64, mutation Mutation) (Identity, error) {
	return s.mutate(ctx, expectedRevision, mutation, "product.logo.deleted", map[string]any{"removed": true}, MutationRequest{Kind: MutationDeleteLogo})
}

// ResetIdentity restores the community-edition identity in a single audited
// revision so callers never observe a default name paired with a stale logo.
func (s *Service) ResetIdentity(ctx context.Context, expectedRevision int64, mutation Mutation) (Identity, error) {
	return s.mutate(ctx, expectedRevision, mutation, "product.identity.reset", map[string]any{"fields": []string{"displayName", "logo"}}, MutationRequest{Kind: MutationReset})
}

func (s *Service) OpenLogo(ctx context.Context, digest string) (io.ReadCloser, Logo, error) {
	identity, err := s.Get(ctx)
	if err != nil {
		return nil, Logo{}, err
	}
	digest = strings.ToLower(strings.TrimSpace(digest))
	if identity.Logo == nil || identity.Logo.SHA256 != digest {
		return nil, Logo{}, ErrNotFound
	}
	reader, err := s.blobs.Open(ctx, digest)
	if errors.Is(err, ErrNotFound) {
		return nil, Logo{}, ErrNotFound
	}
	if err != nil {
		return nil, Logo{}, fmt.Errorf("open product logo: %w", err)
	}
	return reader, *identity.Logo, nil
}

func (s *Service) mutate(ctx context.Context, expectedRevision int64, mutation Mutation, action string, metadata any, request MutationRequest) (Identity, error) {
	if s == nil || s.storage == nil {
		return Identity{}, ErrInvalid
	}
	operationID, generatedCommand := apigencommand.OperationID(ctx)
	if !generatedCommand {
		metadataJSON, err := encodeProductAuditMetadata(metadata)
		if err != nil {
			return Identity{}, err
		}
		request.ExpectedRevision, request.Mutation, request.Action, request.MetadataJSON = expectedRevision, mutation, action, metadataJSON
		return s.storage.Mutate(ctx, request)
	}
	if s.commands == nil {
		return Identity{}, fmt.Errorf("product command executor is unavailable")
	}
	// API command idempotency/replay is resolved by the command executor before
	// this callback. The callback delegates to the configured storage boundary;
	// a native PostgreSQL storage owns exactly one pgx transaction, so no
	// legacy SQLite transaction can wrap a production mutation.
	var identity Identity
	err := s.commands.Execute(ctx, operationID, apigencommand.Execution{
		Transactional: func(ctx context.Context, contract apigencommand.Contract) error {
			if action != contract.AuditAction {
				return fmt.Errorf("generated audit action %q does not match product mutation action %q", contract.AuditAction, action)
			}
			metadataJSON, encodeErr := apigenaudit.EncodeForAudit(*contract.AuditPayload, metadata)
			if encodeErr != nil {
				return encodeErr
			}
			request.ExpectedRevision, request.Mutation, request.Action, request.MetadataJSON = expectedRevision, mutation, action, metadataJSON
			request.CheckConcurrency = func(ctx context.Context, currentRevision int64) error {
				return s.commands.CheckConcurrency(ctx, operationID, mutation.ConcurrencyToken, revisionETag(currentRevision))
			}
			var mutationErr error
			identity, mutationErr = s.storage.Mutate(ctx, request)
			return mutationErr
		},
	})
	return identity, err
}

func encodeProductAuditMetadata(metadata any) (string, error) {
	if metadata == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func inspectLogo(contentType string, raw []byte) (Logo, error) {
	contentType = strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return Logo{}, fmt.Errorf("%w: logo could not be decoded", ErrInvalid)
	}
	mediaTypes := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "webp": "image/webp"}
	mediaType, ok := mediaTypes[format]
	if !ok || mediaType != contentType {
		return Logo{}, fmt.Errorf("%w: Content-Type must match a JPEG, PNG, or WebP image", ErrInvalid)
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width) > math.MaxInt32 || int64(config.Height) > math.MaxInt32 || config.Height > MaxLogoPixels || config.Width > MaxLogoPixels/config.Height {
		return Logo{}, fmt.Errorf("%w: logo exceeds %d pixels", ErrInvalid, MaxLogoPixels)
	}
	if _, decodedFormat, err := image.Decode(bytes.NewReader(raw)); err != nil || decodedFormat != format {
		return Logo{}, fmt.Errorf("%w: logo could not be decoded", ErrInvalid)
	}
	digest := sha256.Sum256(raw)
	return Logo{SHA256: hex.EncodeToString(digest[:]), MediaType: mediaType, SizeBytes: int64(len(raw)), Width: config.Width, Height: config.Height}, nil
}

type scanner interface{ Scan(...any) error }

func scanIdentity(row scanner) (Identity, error) {
	var identity Identity
	var digest, mediaType sql.NullString
	var size, width, height sql.NullInt64
	if err := row.Scan(&identity.DisplayName, &digest, &mediaType, &size, &width, &height, &identity.Revision, &identity.UpdatedAt); err != nil {
		return Identity{}, err
	}
	if digest.Valid {
		identity.Logo = &Logo{SHA256: digest.String, MediaType: mediaType.String, SizeBytes: size.Int64, Width: int(width.Int64), Height: int(height.Int64)}
	}
	return identity, nil
}
