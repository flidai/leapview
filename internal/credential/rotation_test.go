package credential

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential/encryption"
)

type rotationMemory struct {
	rows         []EnvelopeRewrap
	reservations int
	writes       int
	failAt       int
	preflight    error
	audits       []access.AuditIntent
}

func (r *rotationMemory) ReserveEncryption(context.Context, string, string, encryption.KeyCommitment) error {
	r.reservations++
	return nil
}
func (r *rotationMemory) CheckKeyring(context.Context, *encryption.Keyring) error { return r.preflight }
func (r *rotationMemory) ListEnvelopeRewraps(_ context.Context, deployment, key string, limit int) ([]EnvelopeRewrap, error) {
	var selected []EnvelopeRewrap
	for _, row := range r.rows {
		if row.Stored.Metadata.Binding.DeploymentID == deployment && row.Stored.Envelope.KeyID != key {
			selected = append(selected, row)
			if len(selected) == limit {
				break
			}
		}
	}
	return selected, nil
}
func (r *rotationMemory) RewrapEnvelope(_ context.Context, previous EnvelopeRewrap, next encryption.Envelope, audit access.AuditIntent) error {
	r.writes++
	if r.writes == r.failAt {
		return ErrUnavailable
	}
	for index, row := range r.rows {
		if row.Stored.Metadata.Binding.VersionID == previous.Stored.Metadata.Binding.VersionID {
			if row.Revision != previous.Revision {
				return ErrConflict
			}
			r.rows[index].Stored.Envelope = next
			r.rows[index].Revision++
			r.audits = append(r.audits, audit)
			return nil
		}
	}
	return ErrNotFound
}
func rotationTestKeys(t *testing.T, active string) *encryption.Keyring {
	t.Helper()
	entries := []map[string]string{}
	for index, id := range []string{"old-key", "new-key"} {
		state := "decrypt_only"
		if id == active {
			state = "active_write"
		}
		entries = append(entries, map[string]string{"key_id": id, "key_base64": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{byte(index + 1)}, 32)), "state": state})
	}
	body, _ := json.Marshal(map[string]any{"format": "credential-keyring-v1", "deployment_id": "deployment", "active_write_key_id": active, "keys": entries})
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "keyring.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	keys, err := encryption.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}
func TestEnvelopeRotationResumesWithoutChangingVersionOrBinding(t *testing.T) {
	repo := &rotationMemory{failAt: 2}
	old := rotationTestKeys(t, "old-key")
	next := rotationTestKeys(t, "new-key")
	for _, id := range []string{"first", "second"} {
		binding := encryption.Binding{DeploymentID: "deployment", OwnerID: "customer", ScopeKind: "agent", ResourceID: "instance", Purpose: "agent", Provider: "provider", Destination: "sha256:" + string(bytes.Repeat([]byte{'a'}, 64)), VersionID: id}
		envelope, err := old.Encrypt(t.Context(), repo, binding, []byte("private-credential"))
		if err != nil {
			t.Fatal(err)
		}
		repo.rows = append(repo.rows, EnvelopeRewrap{Stored: StoredVersion{Metadata: Metadata{Binding: binding}, Envelope: envelope}, Revision: 1})
	}
	original := repo.rows[0].Stored.Metadata.Binding
	progress, err := RotateEnvelopes(t.Context(), repo, next, 10)
	if !errors.Is(err, ErrUnavailable) || progress.Rewrapped != 1 || repo.reservations != 4 {
		t.Fatalf("interrupted rotation: %+v %v budget=%d", progress, err, repo.reservations)
	}
	if repo.rows[0].Revision != 2 || repo.rows[1].Revision != 1 {
		t.Fatal("interruption lost committed progress")
	}
	repo.failAt = 0
	progress, err = RotateEnvelopes(t.Context(), repo, next, 10)
	if err != nil || progress.Rewrapped != 1 || !progress.Complete || repo.reservations != 5 {
		t.Fatalf("resume: %+v %v budget=%d", progress, err, repo.reservations)
	}
	if repo.rows[0].Stored.Metadata.Binding != original || repo.rows[0].Revision != 2 {
		t.Fatal("resume rewrote immutable identity or an already rotated envelope")
	}
	for _, row := range repo.rows {
		plain, e := next.Decrypt(row.Stored.Metadata.Binding, row.Stored.Envelope)
		if e != nil || string(plain) != "private-credential" {
			t.Fatal("rewrap lost exact AAD or plaintext")
		}
		clear(plain)
	}
	encoded, _ := json.Marshal(repo.audits)
	if bytes.Contains(encoded, []byte("private-credential")) || len(repo.audits) != 2 {
		t.Fatal("audit leaked secret or lost completed envelopes")
	}
	repo.preflight = ErrUnavailable
	before := repo.reservations
	if _, err = RotateEnvelopes(t.Context(), repo, next, 10); !errors.Is(err, ErrUnavailable) || repo.reservations != before {
		t.Fatal("failed key coverage reached encryption")
	}
}
