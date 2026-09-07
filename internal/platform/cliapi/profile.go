package cliapi

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
)

const profileDocumentVersion = 1

var (
	ErrProfileNotFound          = errors.New("target profile not found")
	ErrProjectAuthorityConflict = errors.New("project authority identity conflicts with durable state")
)

// ProjectAuthority is the singleton issuer record owned by this local
// deployment authority. It is independent of source roots, target profiles,
// repositories, and serving environments.
type ProjectAuthority struct {
	IssuerID   string `json:"issuerId"`
	ProjectUID string `json:"projectUid"`
}

// TargetProfile contains target identity and a reference to a native-store
// account. Secret material is never part of this document.
type TargetProfile struct {
	Origin            string `json:"origin"`
	InstanceID        string `json:"instanceId"`
	Environment       string `json:"environment,omitempty"`
	ProjectID         string `json:"projectId"`
	CredentialAccount string `json:"credentialAccount"`
}

type NamedTargetProfile struct {
	Name    string
	Profile TargetProfile
}

type profileDocument struct {
	Version          int                      `json:"version"`
	ProjectAuthority *ProjectAuthority        `json:"projectAuthority,omitempty"`
	Targets          map[string]TargetProfile `json:"targets"`
}

// ProfileStore persists non-secret CLI target metadata in a versioned document.
type ProfileStore struct {
	path string
	mu   sync.Mutex
}

func NewProfileStore(path string) *ProfileStore {
	return &ProfileStore{path: path}
}

// ResolveProjectAuthority returns the durable singleton ProjectUID. On first
// use it persists either the explicitly issued UID or a newly minted opaque
// UID under the same cross-process lock used for target metadata. Existing
// authority state can only be replayed exactly; it is never replaced.
func (store *ProfileStore) ResolveProjectAuthority(externallyIssuedUID string, validateResourceID func(string) error) (ProjectAuthority, error) {
	if validateResourceID == nil {
		return ProjectAuthority{}, errors.New("project authority ResourceID validator is required")
	}
	externallyIssuedUID = strings.TrimSpace(externallyIssuedUID)
	if externallyIssuedUID != "" {
		if err := validateResourceID(externallyIssuedUID); err != nil {
			return ProjectAuthority{}, fmt.Errorf("validate externally issued ProjectUID: %w", err)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	lock, err := store.acquireProjectAuthorityLock()
	if err != nil {
		return ProjectAuthority{}, err
	}
	defer lock.Release()
	document, err := store.loadProjectAuthority(validateResourceID)
	if err != nil {
		return ProjectAuthority{}, err
	}
	if document.ProjectAuthority != nil {
		if externallyIssuedUID != "" && document.ProjectAuthority.ProjectUID != externallyIssuedUID {
			return ProjectAuthority{}, fmt.Errorf("%w: stored %q, requested %q", ErrProjectAuthorityConflict, document.ProjectAuthority.ProjectUID, externallyIssuedUID)
		}
		return *document.ProjectAuthority, nil
	}
	issuerID, err := mintAuthorityResourceID("lvissuer_", validateResourceID)
	if err != nil {
		return ProjectAuthority{}, err
	}
	projectUID := externallyIssuedUID
	if projectUID == "" {
		projectUID, err = mintAuthorityResourceID("lvproject_", validateResourceID)
		if err != nil {
			return ProjectAuthority{}, err
		}
	}
	authority := ProjectAuthority{IssuerID: issuerID, ProjectUID: projectUID}
	document.ProjectAuthority = &authority
	if err := store.save(document); err != nil {
		return ProjectAuthority{}, err
	}
	return authority, nil
}

func (store *ProfileStore) acquireProjectAuthorityLock() (*instancelock.Lock, error) {
	var lastErr error
	for attempt := 0; attempt < 100; attempt++ {
		lock, err := store.acquireMutationLock()
		if err == nil {
			return lock, nil
		}
		lastErr = err
		time.Sleep(5 * time.Millisecond)
	}
	return nil, fmt.Errorf("acquire project authority state lock: %w", lastErr)
}

func mintAuthorityResourceID(prefix string, validateResourceID func(string) error) (string, error) {
	var entropy [24]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("mint project authority identity: %w", err)
	}
	value := prefix + base64.RawURLEncoding.EncodeToString(entropy[:])
	if err := validateResourceID(value); err != nil {
		return "", fmt.Errorf("mint project authority identity: %w", err)
	}
	return value, nil
}

func (store *ProfileStore) loadProjectAuthority(validateResourceID func(string) error) (profileDocument, error) {
	document, err := store.load()
	if err != nil {
		return profileDocument{}, err
	}
	if document.ProjectAuthority != nil {
		if err := validateResourceID(document.ProjectAuthority.IssuerID); err != nil {
			return profileDocument{}, fmt.Errorf("stored project issuer identity is invalid: %w", err)
		}
		if err := validateResourceID(document.ProjectAuthority.ProjectUID); err != nil {
			return profileDocument{}, fmt.Errorf("stored ProjectUID is invalid: %w", err)
		}
	}
	return document, nil
}

func (store *ProfileStore) Get(name string) (TargetProfile, error) {
	document, err := store.load()
	if err != nil {
		return TargetProfile{}, err
	}
	profile, ok := document.Targets[strings.TrimSpace(name)]
	if !ok {
		return TargetProfile{}, ErrProfileNotFound
	}
	return profile, nil
}

func (store *ProfileStore) FindByOrigin(origin string) (string, TargetProfile, error) {
	profiles, err := store.ProfilesByOrigin(origin)
	if err != nil {
		return "", TargetProfile{}, err
	}
	if len(profiles) == 0 {
		return "", TargetProfile{}, ErrProfileNotFound
	}
	if len(profiles) != 1 {
		return "", TargetProfile{}, fmt.Errorf("multiple target profiles use origin %q", origin)
	}
	return profiles[0].Name, profiles[0].Profile, nil
}

func (store *ProfileStore) ProfilesByOrigin(origin string) ([]NamedTargetProfile, error) {
	canonical, err := canonicalTargetOrigin(origin)
	if err != nil {
		return nil, err
	}
	document, err := store.load()
	if err != nil {
		return nil, err
	}
	profiles := make([]NamedTargetProfile, 0)
	for name, profile := range document.Targets {
		if profile.Origin == canonical {
			profiles = append(profiles, NamedTargetProfile{Name: name, Profile: profile})
		}
	}
	return profiles, nil
}

func (store *ProfileStore) Put(name string, profile TargetProfile) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("target profile name is required")
	}
	canonical, err := canonicalTargetOrigin(profile.Origin)
	if err != nil {
		return err
	}
	profile.Origin = canonical
	if strings.TrimSpace(profile.InstanceID) == "" {
		return fmt.Errorf("target instance identity is required")
	}
	if strings.TrimSpace(profile.ProjectID) == "" {
		return fmt.Errorf("target project is required")
	}
	if strings.TrimSpace(profile.CredentialAccount) == "" {
		return fmt.Errorf("target credential account is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	lock, err := store.acquireMutationLock()
	if err != nil {
		return err
	}
	defer lock.Release()
	document, err := store.load()
	if err != nil {
		return err
	}
	if current, ok := document.Targets[name]; ok {
		if current.InstanceID != profile.InstanceID {
			return fmt.Errorf("target profile %q instance identity changed from %q to %q", name, current.InstanceID, profile.InstanceID)
		}
		if current.Origin != profile.Origin || current.ProjectID != profile.ProjectID {
			return fmt.Errorf("target profile %q origin or project changed; delete it before replacement", name)
		}
	}
	document.Targets[name] = profile
	return store.save(document)
}

func (store *ProfileStore) Delete(name string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	lock, err := store.acquireMutationLock()
	if err != nil {
		return err
	}
	defer lock.Release()
	document, err := store.load()
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if _, ok := document.Targets[name]; !ok {
		return ErrProfileNotFound
	}
	delete(document.Targets, name)
	return store.save(document)
}

func (store *ProfileStore) load() (profileDocument, error) {
	document := profileDocument{Version: profileDocumentVersion, Targets: map[string]TargetProfile{}}
	content, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return document, nil
	}
	if err != nil {
		return profileDocument{}, fmt.Errorf("read target profiles: %w", err)
	}
	var raw any
	if err := json.Unmarshal(content, &raw); err != nil {
		return profileDocument{}, fmt.Errorf("decode target profiles: %w", err)
	}
	if field := secretBearingField(raw); field != "" {
		return profileDocument{}, fmt.Errorf("target profile contains forbidden secret-bearing field %q; remove it and log in again", field)
	}
	if err := json.Unmarshal(content, &document); err != nil {
		return profileDocument{}, fmt.Errorf("decode target profiles: %w", err)
	}
	if document.Version != profileDocumentVersion {
		return profileDocument{}, fmt.Errorf("unsupported target profile version %d", document.Version)
	}
	if document.Targets == nil {
		document.Targets = map[string]TargetProfile{}
	}
	for name, profile := range document.Targets {
		canonical, err := canonicalTargetOrigin(profile.Origin)
		if err != nil {
			return profileDocument{}, fmt.Errorf("target profile %q: %w", name, err)
		}
		profile.Origin = canonical
		document.Targets[name] = profile
	}
	return document, nil
}

func (store *ProfileStore) save(document profileDocument) error {
	if strings.TrimSpace(store.path) == "" {
		return fmt.Errorf("target profile path is required")
	}
	content, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode target profiles: %w", err)
	}
	if err := securefs.WritePrivateFileAtomic(store.path, content); err != nil {
		return fmt.Errorf("write target profiles: %w", err)
	}
	return nil
}

func (store *ProfileStore) acquireMutationLock() (*instancelock.Lock, error) {
	if strings.TrimSpace(store.path) == "" {
		return nil, fmt.Errorf("target profile path is required")
	}
	return instancelock.AcquireNamed(
		filepath.Dir(store.path),
		"."+filepath.Base(store.path)+".lock",
	)
}

func canonicalTargetOrigin(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("target origin must be an absolute URL")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("target origin must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("target origin must not contain a path, query, or fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return "", fmt.Errorf("target origin must use HTTPS (HTTP is allowed only for loopback development)")
	}
	parsed.Path = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return net.ParseIP(host).IsLoopback()
}

func secretBearingField(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			switch normalized {
			case "token", "apitoken", "accesstoken", "refreshtoken", "password", "secret", "clientsecret":
				return key
			}
			if field := secretBearingField(nested); field != "" {
				return field
			}
		}
	case []any:
		for _, nested := range typed {
			if field := secretBearingField(nested); field != "" {
				return field
			}
		}
	}
	return ""
}
