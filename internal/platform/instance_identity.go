package platform

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
)

const instanceIDSetting = "instance.id"

// InstanceID returns the durable, non-secret identity of this LeapView
// installation. It survives process restarts and remains stable for the
// lifetime of the installation.
func (s *Store) InstanceID(ctx context.Context) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("platform store is not open")
	}
	value, err := s.GetSetting(ctx, instanceIDSetting)
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read instance ID: %w", err)
	}
	var entropy [24]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("generate instance ID: %w", err)
	}
	candidate := "lvinst_" + base64.RawURLEncoding.EncodeToString(entropy[:])
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO platform_settings (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO NOTHING
	`, instanceIDSetting, candidate); err != nil {
		return "", fmt.Errorf("persist instance ID: %w", err)
	}
	value, err = s.GetSetting(ctx, instanceIDSetting)
	if err != nil {
		return "", fmt.Errorf("verify instance ID: %w", err)
	}
	return value, nil
}
