package gcstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

const localOwnershipMarker = ".leapview-pool-owner.json"
const localDeletionLease = ".leapview-pool-gc-lease.json"
const localDeletionLeaseLock = ".leapview-pool-gc-lease.lock"

type ownershipMarker struct {
	PoolID              physicalpool.PoolID `json:"pool_id"`
	CompatibilityDigest string              `json:"compatibility_digest"`
	EvidenceDigest      string              `json:"evidence_digest"`
	OwnerID             string              `json:"owner_id"`
}

type deletionLease struct {
	OwnerID   string    `json:"owner_id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func markerFor(claim physicalpool.OwnershipClaim) ownershipMarker {
	return ownershipMarker{PoolID: claim.PoolID, CompatibilityDigest: claim.CompatibilityDigest, EvidenceDigest: claim.EvidenceDigest, OwnerID: claim.OwnerID}
}

func (s *LocalStore) AcquireNamespaceOwnership(_ context.Context, claim physicalpool.OwnershipClaim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	if s == nil || s.Root == "" {
		return physicalpool.ErrOwnershipConflict
	}
	encoded, err := json.Marshal(markerFor(claim))
	if err != nil {
		return err
	}
	path := filepath.Join(s.Root, localOwnershipMarker)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if _, writeErr := file.Write(encoded); writeErr != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return writeErr
		}
		if syncErr := file.Sync(); syncErr != nil {
			_ = file.Close()
			return fmt.Errorf("sync physical-pool ownership marker: %w", syncErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return closeErr
		}
		// Sync the containing directory so a power loss cannot acknowledge a
		// marker whose name has not reached the directory entry.
		directory, openErr := os.Open(s.Root)
		if openErr != nil {
			return fmt.Errorf("open physical-pool namespace for marker sync: %w", openErr)
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil {
			return fmt.Errorf("sync physical-pool namespace marker entry: %w", syncErr)
		}
		return closeErr
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	return s.VerifyNamespaceOwnership(context.Background(), claim)
}

func (s *LocalStore) VerifyNamespaceOwnership(_ context.Context, claim physicalpool.OwnershipClaim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	path := filepath.Join(s.Root, localOwnershipMarker)
	encoded, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read physical-pool ownership marker: %w", err)
	}
	var marker ownershipMarker
	if err := json.Unmarshal(encoded, &marker); err != nil {
		return physicalpool.ErrOwnershipConflict
	}
	// Compatibility/evidence digests are informational admission evidence. A
	// stable namespace marker must survive runtime/extension upgrades, so the
	// authority check is the immutable pool plus durable database owner.
	want := markerFor(claim)
	if marker.PoolID != want.PoolID || marker.OwnerID != want.OwnerID {
		return physicalpool.ErrOwnershipConflict
	}
	return nil
}

func (s *LocalStore) AcquireNamespaceDeletionLease(_ context.Context, ownerID string, ttl time.Duration) (string, error) {
	if s == nil || s.Root == "" || ownerID == "" || ttl <= 0 {
		return "", physicalpool.ErrDeletionLeaseConflict
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	lease := deletionLease{OwnerID: ownerID, Token: fmt.Sprintf("%x", tokenBytes), ExpiresAt: time.Now().UTC().Add(ttl)}
	encoded, err := json.Marshal(lease)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.Root, localDeletionLease)
	lock, lockErr := lockLeaseFile(s.Root)
	if lockErr != nil {
		return "", lockErr
	}
	defer unlockLeaseFile(lock)
	for attempt := 0; attempt < 2; attempt++ {
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr == nil {
			if _, writeErr := file.Write(encoded); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return "", writeErr
			}
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				return "", syncErr
			}
			if closeErr := file.Close(); closeErr != nil {
				return "", closeErr
			}
			directory, openErr := os.Open(s.Root)
			if openErr != nil {
				return "", openErr
			}
			syncErr := directory.Sync()
			closeErr := directory.Close()
			if syncErr != nil {
				return "", syncErr
			}
			return lease.Token, closeErr
		}
		if !errors.Is(createErr, os.ErrExist) {
			return "", createErr
		}
		current, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", physicalpool.ErrDeletionLeaseConflict
		}
		var held deletionLease
		if json.Unmarshal(current, &held) != nil || held.ExpiresAt.After(time.Now().UTC()) {
			return "", physicalpool.ErrDeletionLeaseConflict
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return "", physicalpool.ErrDeletionLeaseConflict
		}
	}
	return "", physicalpool.ErrDeletionLeaseConflict
}

func (s *LocalStore) VerifyNamespaceDeletionLease(_ context.Context, ownerID, token string) error {
	if s == nil || ownerID == "" || token == "" {
		return physicalpool.ErrDeletionLeaseConflict
	}
	encoded, err := os.ReadFile(filepath.Join(s.Root, localDeletionLease))
	if err != nil {
		return physicalpool.ErrDeletionLeaseConflict
	}
	var held deletionLease
	if json.Unmarshal(encoded, &held) != nil || held.OwnerID != ownerID || held.Token != token || !held.ExpiresAt.After(time.Now().UTC()) {
		return physicalpool.ErrDeletionLeaseConflict
	}
	return nil
}

func (s *LocalStore) ReleaseNamespaceDeletionLease(_ context.Context, ownerID, token string) error {
	if s == nil || ownerID == "" || token == "" {
		return nil
	}
	lock, lockErr := lockLeaseFile(s.Root)
	if lockErr != nil {
		return lockErr
	}
	defer unlockLeaseFile(lock)
	encoded, readErr := os.ReadFile(filepath.Join(s.Root, localDeletionLease))
	if readErr != nil {
		return nil
	}
	var held deletionLease
	if json.Unmarshal(encoded, &held) != nil || held.OwnerID != ownerID || held.Token != token || !held.ExpiresAt.After(time.Now().UTC()) {
		return physicalpool.ErrDeletionLeaseConflict
	}
	if err := os.Remove(filepath.Join(s.Root, localDeletionLease)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func lockLeaseFile(root string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(root, localDeletionLeaseLock), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func unlockLeaseFile(file *os.File) {
	if file == nil {
		return
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}
