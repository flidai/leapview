// Package cursorsigning owns the process-wide key ring used by public API
// cursors. The application configures it from durable instance state before
// serving requests; all cursor domains include their prefix in the MAC.
package cursorsigning

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"sync/atomic"
)

// Initializer loads or creates the durable process key ring before serving
// requests. Implementations are owned by a storage capability (for example,
// the PostgreSQL repository); the protocol depends only on this port.
type Initializer interface {
	Configure(context.Context) error
}

// InitializerFunc adapts a function to Initializer for tests and composition
// roots that already own a repository lifecycle.
type InitializerFunc func(context.Context) error

func (f InitializerFunc) Configure(ctx context.Context) error {
	if f == nil {
		return fmt.Errorf("cursor signing initializer is nil")
	}
	return f(ctx)
}

// NewEphemeralInitializer returns an explicit process-local initializer for
// profile-only assemblies. It intentionally creates a fresh random key ring
// when configured; production composition should inject a durable authority.
func NewEphemeralInitializer() Initializer {
	return InitializerFunc(func(context.Context) error {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return fmt.Errorf("initialize ephemeral cursor signing key: %w", err)
		}
		return Configure("ephemeral", map[string][]byte{"ephemeral": secret})
	})
}

type ring struct {
	current string
	keys    map[string][]byte
}

var configured atomic.Pointer[ring]

func init() {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic(fmt.Sprintf("initialize cursor signing key: %v", err))
	}
	configured.Store(&ring{current: "ephemeral", keys: map[string][]byte{"ephemeral": secret}})
}

// Configure installs a key ring. The current key signs new cursors; every key
// in the ring can verify existing cursors during rotation.
func Configure(current string, keys map[string][]byte) error {
	current = strings.TrimSpace(current)
	if current == "" || strings.Contains(current, ".") {
		return fmt.Errorf("cursor signing key id is invalid")
	}
	copyKeys := make(map[string][]byte, len(keys))
	for id, key := range keys {
		id = strings.TrimSpace(id)
		if id == "" || strings.Contains(id, ".") || len(key) < 32 {
			return fmt.Errorf("cursor signing key %q is invalid", id)
		}
		copyKeys[id] = append([]byte(nil), key...)
	}
	if _, ok := copyKeys[current]; !ok {
		return fmt.Errorf("current cursor signing key %q is absent", current)
	}
	configured.Store(&ring{current: current, keys: copyKeys})
	return nil
}

func Sign(prefix string, payload []byte) string {
	current := configured.Load()
	key := current.keys[current.current]
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(prefix))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(current.current))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	value := append(append([]byte(nil), payload...), mac.Sum(nil)...)
	return prefix + "." + current.current + "." + base64.RawURLEncoding.EncodeToString(value)
}

func Verify(prefix, token string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != prefix || parts[1] == "" {
		return nil, fmt.Errorf("invalid cursor")
	}
	key := configured.Load().keys[parts[1]]
	if len(key) == 0 {
		return nil, fmt.Errorf("unknown cursor signing key %q", parts[1])
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(raw) <= sha256.Size {
		return nil, fmt.Errorf("invalid cursor")
	}
	payload, signature := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(prefix))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(parts[1]))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, fmt.Errorf("invalid cursor signature")
	}
	return payload, nil
}
