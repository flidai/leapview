package localruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/google/uuid"
)

const (
	attachmentsFileName                = "attachments.json"
	attachmentsLockName                = ".attachments.lock"
	attachmentSchemaVersion            = 1
	defaultAttachmentHeartbeatInterval = 5 * time.Second
	defaultAttachmentStaleAfter        = 30 * time.Second
)

var (
	ErrAttachmentLost  = errors.New("local development attachment lease was lost")
	ErrLiveAttachments = errors.New("live local development attachments remain")
)

type attachmentBinding struct {
	OperationID string `json:"operationId"`
	CheckoutID  string `json:"checkoutId"`
	OwnerID     string `json:"ownerId"`
}

type attachmentRecord struct {
	ID          string    `json:"id"`
	TokenDigest string    `json:"tokenDigest"`
	PID         int       `json:"pid"`
	AttachedAt  time.Time `json:"attachedAt"`
	HeartbeatAt time.Time `json:"heartbeatAt"`
}

type attachmentRegistry struct {
	SchemaVersion int                `json:"schemaVersion"`
	Binding       attachmentBinding  `json:"binding"`
	Attachments   []attachmentRecord `json:"attachments"`
}

// Attachment is a process-held capability for one renewable CLI attachment.
// Only its digest is persisted, so a saved PID or copied record cannot renew it.
type Attachment struct {
	id      string
	token   string
	root    string
	binding attachmentBinding
}

func (attachment *Attachment) ID() string {
	if attachment == nil {
		return ""
	}
	return attachment.id
}

func attachmentBindingFor(state State) attachmentBinding {
	return attachmentBinding{
		OperationID: state.OperationID,
		CheckoutID:  state.Checkout.ID,
		OwnerID:     state.Runtime.OwnerID,
	}
}

func loadAttachmentRegistry(path string, binding attachmentBinding, allowMissing bool) (attachmentRegistry, error) {
	encoded, err := securefs.ReadPrivateFile(path)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return attachmentRegistry{SchemaVersion: attachmentSchemaVersion, Binding: binding, Attachments: []attachmentRecord{}}, nil
	}
	if err != nil {
		return attachmentRegistry{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var registry attachmentRegistry
	if err := decoder.Decode(&registry); err != nil {
		return attachmentRegistry{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return attachmentRegistry{}, errors.New("trailing JSON value")
	}
	if registry.SchemaVersion != attachmentSchemaVersion || registry.Binding != binding {
		return attachmentRegistry{}, errors.New("registry identity does not match the selected runtime")
	}
	seen := make(map[string]struct{}, len(registry.Attachments))
	for _, record := range registry.Attachments {
		parsed, parseErr := uuid.Parse(record.ID)
		if parseErr != nil || parsed == uuid.Nil || parsed.String() != record.ID || record.PID <= 0 ||
			record.TokenDigest != canonicalAttachmentDigest(record.TokenDigest) || record.AttachedAt.IsZero() ||
			record.HeartbeatAt.Before(record.AttachedAt) {
			return attachmentRegistry{}, errors.New("registry contains an invalid attachment record")
		}
		if _, duplicate := seen[record.ID]; duplicate {
			return attachmentRegistry{}, errors.New("registry contains duplicate attachment identities")
		}
		seen[record.ID] = struct{}{}
	}
	return registry, nil
}

func saveAttachmentRegistry(path string, registry attachmentRegistry) error {
	sort.Slice(registry.Attachments, func(i, j int) bool { return registry.Attachments[i].ID < registry.Attachments[j].ID })
	encoded, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(path, append(encoded, '\n'))
}

func canonicalAttachmentDigest(value string) string {
	if len(value) != len("sha256:")+sha256.Size*2 || value[:len("sha256:")] != "sha256:" || strings.ToLower(value) != value {
		return ""
	}
	decoded, err := hex.DecodeString(value[len("sha256:"):])
	if err != nil || len(decoded) != sha256.Size {
		return ""
	}
	return value
}

func attachmentTokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func classifyAttachments(registry attachmentRegistry, now time.Time, staleAfter time.Duration) (live []attachmentRecord, stale int, err error) {
	now = now.UTC()
	for _, record := range registry.Attachments {
		if record.HeartbeatAt.After(now) {
			return nil, 0, errors.New("attachment heartbeat is in the future")
		}
		if now.Sub(record.HeartbeatAt) > staleAfter {
			stale++
			continue
		}
		live = append(live, record)
	}
	return live, stale, nil
}

func acquireAttachmentLock(ctx context.Context, root string) (*instancelock.Lock, error) {
	return acquireRuntimeLock(ctx, root, attachmentsLockName, 2*time.Second, "local development attachment")
}

func (controller *Controller) registerAttachment(ctx context.Context, root string, state State) (*Attachment, error) {
	lock, err := acquireAttachmentLock(ctx, root)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	binding := attachmentBindingFor(state)
	path := filepath.Join(root, attachmentsFileName)
	registry, err := loadAttachmentRegistry(path, binding, false)
	if err != nil {
		return nil, fmt.Errorf("local attachment ownership is uncertain; refusing registration: %w", err)
	}
	live, _, err := classifyAttachments(registry, controller.now(), controller.attachmentStaleAfter)
	if err != nil {
		return nil, fmt.Errorf("local attachment ownership is uncertain; refusing registration: %w", err)
	}
	token, err := randomToken("lvattach_", 32)
	if err != nil {
		return nil, err
	}
	now := controller.now().UTC()
	record := attachmentRecord{ID: uuid.NewString(), TokenDigest: attachmentTokenDigest(token), PID: os.Getpid(), AttachedAt: now, HeartbeatAt: now}
	registry.Attachments = append(live, record)
	if err := saveAttachmentRegistry(path, registry); err != nil {
		return nil, err
	}
	return &Attachment{id: record.ID, token: token, root: root, binding: binding}, nil
}

func (controller *Controller) Heartbeat(ctx context.Context, attachment *Attachment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if attachment == nil || attachment.id == "" || attachment.token == "" || attachment.root == "" {
		return ErrAttachmentLost
	}
	state, exists, err := loadState(filepath.Join(attachment.root, stateFileName))
	if err != nil || !exists || state.AttachmentRegistryVersion != attachmentSchemaVersion || attachmentBindingFor(state) != attachment.binding {
		return ErrAttachmentLost
	}
	lock, err := acquireAttachmentLock(ctx, attachment.root)
	if err != nil {
		return err
	}
	defer lock.Release()
	path := filepath.Join(attachment.root, attachmentsFileName)
	registry, err := loadAttachmentRegistry(path, attachment.binding, false)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAttachmentLost, err)
	}
	live, _, err := classifyAttachments(registry, controller.now(), controller.attachmentStaleAfter)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAttachmentLost, err)
	}
	found := false
	for index := range live {
		if live[index].ID != attachment.id {
			continue
		}
		if live[index].TokenDigest != attachmentTokenDigest(attachment.token) {
			return ErrAttachmentLost
		}
		live[index].HeartbeatAt = controller.now().UTC()
		found = true
		break
	}
	if !found {
		return ErrAttachmentLost
	}
	registry.Attachments = live
	return saveAttachmentRegistry(path, registry)
}

func attachmentStatuses(records []attachmentRecord) []AttachmentStatus {
	statuses := make([]AttachmentStatus, 0, len(records))
	for _, record := range records {
		statuses = append(statuses, AttachmentStatus{ID: record.ID, PID: record.PID, AttachedAt: record.AttachedAt, HeartbeatAt: record.HeartbeatAt})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	return statuses
}
