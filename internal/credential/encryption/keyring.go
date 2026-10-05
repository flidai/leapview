package encryption

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/flidai/leapview/pkg/strictjson"
)

const (
	keyringFormat   = "credential-keyring-v1"
	maxKeyringBytes = 1 << 20
)

type Keyring struct {
	deploymentID   string
	activeWriteKey string
	keys           map[string][32]byte
	commitments    map[string]KeyCommitment
}

// KeyCommitment is a one-way SHA-256 commitment to random key material. It is
// persisted only to prevent a key ID from being rebound across keyring edits.
type KeyCommitment [32]byte

// KeyCommitmentEntry exposes only a key ID and its one-way material
// commitment, never the decoded key bytes.
type KeyCommitmentEntry struct {
	KeyID      string
	Commitment KeyCommitment
}

type keyringDocument struct {
	Format         string          `json:"format"`
	DeploymentID   string          `json:"deployment_id"`
	ActiveWriteKey string          `json:"active_write_key_id"`
	Keys           []keyringKeyDoc `json:"keys"`
}

type keyringKeyDoc struct {
	KeyID     string          `json:"key_id"`
	KeyBase64 json.RawMessage `json:"key_base64"`
	State     string          `json:"state"`
}

// Load reads and validates a versioned keyring from a private, non-symlink file.
func Load(path string) (*Keyring, error) {
	if path == "" || filepath.Clean(path) != path {
		return nil, errors.New("credential keyring path is invalid")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("credential keyring path is invalid")
	}
	contents, err := readPrivateKeyring(absPath)
	if err != nil {
		return nil, errors.New("credential keyring could not be read as a private file")
	}
	defer clear(contents)
	document, err := decodeKeyring(contents)
	defer clearKeyringDocument(&document)
	if err != nil {
		return nil, errors.New("credential keyring document is invalid")
	}
	return validateKeyring(document)
}

func (k *Keyring) DeploymentID() string {
	if k == nil {
		return ""
	}
	return k.deploymentID
}

// KeyCommitment returns a non-secret equality commitment without exposing key
// bytes. The commitment is suitable for storage but must not be returned by an
// API or included in logs.
func (k *Keyring) KeyCommitment(keyID string) (KeyCommitment, bool) {
	if k == nil {
		return KeyCommitment{}, false
	}
	commitment, ok := k.commitments[keyID]
	return commitment, ok
}

// KeyCommitments returns a deterministic projection for startup consistency
// checks. The returned commitments must not be exposed by an API or logged.
func (k *Keyring) KeyCommitments() []KeyCommitmentEntry {
	if k == nil {
		return nil
	}
	entries := make([]KeyCommitmentEntry, 0, len(k.commitments))
	for keyID, commitment := range k.commitments {
		entries = append(entries, KeyCommitmentEntry{KeyID: keyID, Commitment: commitment})
	}
	slices.SortFunc(entries, func(left, right KeyCommitmentEntry) int {
		return strings.Compare(left.KeyID, right.KeyID)
	})
	return entries
}

// String and GoString keep decoded key bytes out of accidental log formatting.
func (k *Keyring) String() string {
	if k == nil {
		return "credential keyring <nil>"
	}
	return "credential keyring deployment=" + k.deploymentID + " key_count=" + strconv.Itoa(len(k.keys))
}

func (k *Keyring) GoString() string { return k.String() }

func validateKeyring(document keyringDocument) (*Keyring, error) {
	if document.Format != keyringFormat || !canonicalValue(document.DeploymentID) || !canonicalValue(document.ActiveWriteKey) || len(document.Keys) == 0 {
		return nil, errors.New("invalid credential keyring metadata")
	}
	keys := make(map[string][32]byte, len(document.Keys))
	commitments := make(map[string]KeyCommitment, len(document.Keys))
	seenKeyMaterial := make(map[[32]byte]struct{}, len(document.Keys))
	keepKeys := false
	defer func() {
		if !keepKeys {
			for keyID := range keys {
				keys[keyID] = [32]byte{}
				commitments[keyID] = KeyCommitment{}
			}
		}
		for key := range seenKeyMaterial {
			delete(seenKeyMaterial, key)
		}
	}()
	activeCount := 0
	for _, item := range document.Keys {
		if !canonicalValue(item.KeyID) {
			return nil, errors.New("invalid credential key identifier")
		}
		if _, duplicate := keys[item.KeyID]; duplicate {
			return nil, errors.New("duplicate credential key identifier")
		}
		if item.State != "active_write" && item.State != "decrypt_only" {
			return nil, errors.New("invalid credential key state")
		}
		key, ok := decodeKeyMaterial(item.KeyBase64)
		if !ok {
			return nil, errors.New("invalid credential key material")
		}
		fingerprint := sha256.Sum256(key[:])
		if _, duplicateMaterial := seenKeyMaterial[fingerprint]; duplicateMaterial {
			clear(key[:])
			return nil, errors.New("duplicate credential key material")
		}
		seenKeyMaterial[fingerprint] = struct{}{}
		keys[item.KeyID] = key
		commitments[item.KeyID] = KeyCommitment(fingerprint)
		clear(key[:])
		if item.State == "active_write" {
			activeCount++
			if item.KeyID != document.ActiveWriteKey {
				return nil, errors.New("active credential key identifier does not match")
			}
		}
	}
	if activeCount != 1 {
		return nil, errors.New("keyring must contain exactly one active write key")
	}
	if _, exists := keys[document.ActiveWriteKey]; !exists {
		return nil, errors.New("active credential key is missing")
	}
	keepKeys = true
	return &Keyring{deploymentID: document.DeploymentID, activeWriteKey: document.ActiveWriteKey, keys: keys, commitments: commitments}, nil
}

func decodeKeyring(contents []byte) (keyringDocument, error) {
	var document keyringDocument
	err := strictjson.DecodeWithOptions(contents, &document, strictjson.Options{MaxBytes: maxKeyringBytes, MaxDepth: 4})
	return document, err
}

func clearKeyringDocument(document *keyringDocument) {
	for index := range document.Keys {
		clear(document.Keys[index].KeyBase64)
	}
}

func decodeKeyMaterial(raw json.RawMessage) ([32]byte, bool) {
	var key [32]byte
	if len(raw) != 46 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return key, false
	}
	encoded := raw[1 : len(raw)-1]
	for _, character := range encoded {
		if !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '+' || character == '/' || character == '=') {
			return key, false
		}
	}
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	defer clear(decoded)
	n, err := base64.StdEncoding.Strict().Decode(decoded, encoded)
	if err != nil || n != len(key) {
		return key, false
	}
	copy(key[:], decoded)
	return key, true
}

func privateKeyringMode(mode os.FileMode) bool {
	return mode == 0o600 || mode == 0o400
}

func ownedByRootOrCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (stat.Uid == 0 || int(stat.Uid) == os.Geteuid())
}

func readPrivateKeyring(path string) ([]byte, error) {
	directory, name := filepath.Dir(path), filepath.Base(path)
	root, err := openDirectoryWithoutSymlinks(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	before, err := root.Lstat(name)
	if err != nil || !validKeyringFileInfo(before) {
		return nil, errors.New("invalid keyring file")
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("open keyring file failed")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !validKeyringFileInfo(after) {
		return nil, errors.New("keyring file changed while opening")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxKeyringBytes+1))
	if err != nil || len(contents) > maxKeyringBytes {
		clear(contents)
		return nil, errors.New("keyring file exceeds size limit")
	}
	return contents, nil
}

func validKeyringFileInfo(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && privateKeyringMode(info.Mode()) &&
		ownedByRootOrCurrentUser(info) && info.Size() >= 0 && info.Size() <= maxKeyringBytes
}

func openDirectoryWithoutSymlinks(path string) (*os.Root, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || filepath.Clean(absolute) != absolute {
		return nil, errors.New("invalid keyring directory")
	}
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, errors.New("open filesystem root failed")
	}
	components := strings.Split(strings.Trim(absolute, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" {
			continue
		}
		before, err := root.Lstat(component)
		if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			return nil, errors.New("keyring directory contains a symlink or non-directory")
		}
		next, err := root.OpenRoot(component)
		if err != nil {
			_ = root.Close()
			return nil, errors.New("open keyring directory failed")
		}
		after, err := next.Stat(".")
		if err != nil || !os.SameFile(before, after) {
			_ = next.Close()
			_ = root.Close()
			return nil, errors.New("keyring directory changed while opening")
		}
		_ = root.Close()
		root = next
	}
	return root, nil
}
