// Package product owns mutable instance identity and safe, read-only product
// administration status. Deployment configuration and secrets are never
// persisted through this package.
package product

import (
	"bytes"
	"context"
	"crypto/rand"
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
	db       *sql.DB
	blobs    BlobStore
	commands CommandExecutor
}

func New(db *sql.DB, blobs BlobStore) (*Service, error) {
	if db == nil {
		return nil, fmt.Errorf("product identity database is required")
	}
	if blobs == nil {
		return nil, fmt.Errorf("product logo blob store is required")
	}
	return &Service{db: db, blobs: blobs}, nil
}

func (s *Service) ConfigureCommandExecutor(executor CommandExecutor) {
	if s != nil {
		s.commands = executor
	}
}

func (s *Service) Get(ctx context.Context) (Identity, error) {
	return scanIdentity(s.db.QueryRowContext(ctx, `
SELECT display_name, logo_sha256, logo_media_type, logo_size_bytes, logo_width, logo_height, revision, updated_at
FROM product_identity WHERE singleton = 1`))
}

func (s *Service) SetDisplayName(ctx context.Context, expectedRevision int64, displayName string, mutation Mutation) (Identity, error) {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || !utf8.ValidString(displayName) || utf8.RuneCountInString(displayName) > 120 {
		return Identity{}, fmt.Errorf("%w: displayName must contain 1 to 120 characters", ErrInvalid)
	}
	return s.mutate(ctx, expectedRevision, mutation, "product.identity.updated", map[string]any{"fields": []string{"displayName"}}, func(tx *sql.Tx) (sql.Result, error) {
		return tx.ExecContext(ctx, `
UPDATE product_identity
SET display_name = ?, revision = revision + 1, updated_at = CURRENT_TIMESTAMP
WHERE singleton = 1 AND revision = ?`, displayName, expectedRevision)
	})
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
	return s.mutate(ctx, expectedRevision, mutation, "product.logo.updated", map[string]any{"sha256": logo.SHA256}, func(tx *sql.Tx) (sql.Result, error) {
		return tx.ExecContext(ctx, `
UPDATE product_identity
SET logo_sha256 = ?, logo_media_type = ?, logo_size_bytes = ?, logo_width = ?, logo_height = ?,
    revision = revision + 1, updated_at = CURRENT_TIMESTAMP
WHERE singleton = 1 AND revision = ?`, logo.SHA256, logo.MediaType, logo.SizeBytes, logo.Width, logo.Height, expectedRevision)
	})
}

func (s *Service) DeleteLogo(ctx context.Context, expectedRevision int64, mutation Mutation) (Identity, error) {
	return s.mutate(ctx, expectedRevision, mutation, "product.logo.deleted", map[string]any{"removed": true}, func(tx *sql.Tx) (sql.Result, error) {
		return tx.ExecContext(ctx, `
UPDATE product_identity
SET logo_sha256 = NULL, logo_media_type = NULL, logo_size_bytes = NULL, logo_width = NULL, logo_height = NULL,
    revision = revision + 1, updated_at = CURRENT_TIMESTAMP
WHERE singleton = 1 AND revision = ? AND logo_sha256 IS NOT NULL`, expectedRevision)
	})
}

// ResetIdentity restores the community-edition identity in a single audited
// revision so callers never observe a default name paired with a stale logo.
func (s *Service) ResetIdentity(ctx context.Context, expectedRevision int64, mutation Mutation) (Identity, error) {
	return s.mutate(ctx, expectedRevision, mutation, "product.identity.reset", map[string]any{"fields": []string{"displayName", "logo"}}, func(tx *sql.Tx) (sql.Result, error) {
		return tx.ExecContext(ctx, `
UPDATE product_identity
SET display_name = ?, logo_sha256 = NULL, logo_media_type = NULL, logo_size_bytes = NULL,
    logo_width = NULL, logo_height = NULL, revision = revision + 1, updated_at = CURRENT_TIMESTAMP
WHERE singleton = 1 AND revision = ?`, DefaultDisplayName, expectedRevision)
	})
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

func (s *Service) mutate(ctx context.Context, expectedRevision int64, mutation Mutation, action string, metadata any, update func(*sql.Tx) (sql.Result, error)) (Identity, error) {
	operationID, generatedCommand := apigencommand.OperationID(ctx)
	if !generatedCommand {
		metadataJSON, err := encodeProductAuditMetadata(metadata)
		if err != nil {
			return Identity{}, err
		}
		return s.mutateAtomic(ctx, expectedRevision, mutation, action, metadataJSON, nil, update)
	}
	if s.commands == nil {
		return Identity{}, fmt.Errorf("product command executor is unavailable")
	}
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
			checkConcurrency := func(ctx context.Context, tx *sql.Tx) error {
				var currentRevision int64
				if err := tx.QueryRowContext(ctx, `SELECT revision FROM product_identity WHERE singleton = 1`).Scan(&currentRevision); err != nil {
					return err
				}
				return s.commands.CheckConcurrency(ctx, operationID, mutation.ConcurrencyToken, revisionETag(currentRevision))
			}
			var mutationErr error
			identity, mutationErr = s.mutateAtomic(ctx, expectedRevision, mutation, action, metadataJSON, checkConcurrency, update)
			return mutationErr
		},
	})
	return identity, err
}

func (s *Service) mutateAtomic(
	ctx context.Context,
	expectedRevision int64,
	mutation Mutation,
	action, metadataJSON string,
	checkConcurrency func(context.Context, *sql.Tx) error,
	update func(*sql.Tx) (sql.Result, error),
) (Identity, error) {
	if expectedRevision <= 0 {
		return Identity{}, ErrPrecondition
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if checkConcurrency != nil {
		if err := checkConcurrency(ctx, tx); err != nil {
			return Identity{}, err
		}
	}
	result, err := update(tx)
	if err != nil {
		return Identity{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Identity{}, err
	}
	if rows != 1 {
		return Identity{}, ErrPrecondition
	}
	if err := insertAudit(ctx, tx, mutation, action, metadataJSON); err != nil {
		return Identity{}, fmt.Errorf("audit product mutation: %w", err)
	}
	identity, err := scanIdentity(tx.QueryRowContext(ctx, `
SELECT display_name, logo_sha256, logo_media_type, logo_size_bytes, logo_width, logo_height, revision, updated_at
FROM product_identity WHERE singleton = 1`))
	if err != nil {
		return Identity{}, err
	}
	if err := tx.Commit(); err != nil {
		return Identity{}, err
	}
	return identity, nil
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
	if config.Width <= 0 || config.Height <= 0 || config.Height > MaxLogoPixels || config.Width > MaxLogoPixels/config.Height {
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

func insertAudit(ctx context.Context, tx *sql.Tx, mutation Mutation, action, metadataJSON string) error {
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO audit_events
  (id, principal_id, action, resource_kind, resource_id, capability, status, request_id, correlation_id, metadata_json)
VALUES (?, NULLIF(?, ''), ?, 'product', 'instance', 'RESOURCE_MANAGE', 'success', ?, ?, ?)`,
		"audit_"+hex.EncodeToString(idBytes[:]), strings.TrimSpace(mutation.PrincipalID), action,
		strings.TrimSpace(mutation.RequestID), strings.TrimSpace(mutation.CorrelationID), metadataJSON)
	return err
}
