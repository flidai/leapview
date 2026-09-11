package storage

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	ErrProviderVersion     = errors.New("provider version observation is invalid")
	ErrObservationConflict = errors.New("provider version observation conflicts")
)

const (
	maxProviderObservationText     = 4096
	MaxProviderVersionObservations = 10_000
)

// ProviderProfileIdentity is trusted configuration for the provider write
// boundary. It deliberately contains no credentials. ProfileID is an opaque
// identity selected by configuration; the remaining fields bind that identity
// to the physical S3 namespace used by the writer.
type ProviderProfileIdentity struct {
	ProfileID       string
	Implementation  string
	AccountIdentity string
	Endpoint        string
	Region          string
	Bucket          string
	Namespace       string
}

// ProviderVersionObservation records facts returned by one successful provider
// write. SHA256 and Size describe the bytes written; VersionID is authoritative
// only because it came from that write response.
type ProviderVersionObservation struct {
	Profile    ProviderProfileIdentity
	ObjectKey  string
	VersionID  string
	SHA256     string
	Size       int64
	CapturedAt time.Time
}

func ValidateProviderProfileIdentity(profile ProviderProfileIdentity) error {
	fields := []struct{ label, value string }{
		{"profile ID", profile.ProfileID}, {"implementation", profile.Implementation},
		{"account identity", profile.AccountIdentity}, {"endpoint", profile.Endpoint},
		{"region", profile.Region}, {"bucket", profile.Bucket},
	}
	for _, field := range fields {
		if err := validateProviderObservationText(field.value, field.label); err != nil {
			return err
		}
	}
	if profile.Implementation != "s3" {
		return fmt.Errorf("%w: provider implementation must be s3", ErrProviderVersion)
	}
	parsed, err := url.Parse(profile.Endpoint)
	if err != nil || !parsed.IsAbs() || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return fmt.Errorf("%w: provider endpoint is not a credential-free absolute URL", ErrProviderVersion)
	}
	if profile.Namespace != "" {
		if err := validateProviderObservationText(profile.Namespace, "namespace"); err != nil {
			return err
		}
		if strings.HasPrefix(profile.Namespace, "/") || strings.HasSuffix(profile.Namespace, "/") {
			return fmt.Errorf("%w: provider namespace must not start or end with slash", ErrProviderVersion)
		}
	}
	return nil
}

func ValidateProviderVersionObservation(observation ProviderVersionObservation) error {
	if err := ValidateProviderProfileIdentity(observation.Profile); err != nil {
		return err
	}
	if err := validateProviderObservationText(observation.ObjectKey, "object key"); err != nil {
		return err
	}
	if observation.Profile.Namespace != "" && !strings.HasPrefix(observation.ObjectKey, observation.Profile.Namespace+"/") {
		return fmt.Errorf("%w: object key is outside provider namespace", ErrProviderVersion)
	}
	if err := ValidateProviderVersionID(observation.VersionID); err != nil {
		return err
	}
	if err := ValidateBlob(Blob{SHA256: observation.SHA256, Size: observation.Size}); err != nil {
		return fmt.Errorf("%w: %v", ErrProviderVersion, err)
	}
	if observation.CapturedAt.IsZero() || observation.CapturedAt.Location() != time.UTC || observation.CapturedAt.Nanosecond()%1_000 != 0 {
		return fmt.Errorf("%w: capture timestamp must be nonzero UTC at microsecond precision", ErrProviderVersion)
	}
	return nil
}

func ValidateProviderVersionID(versionID string) error {
	if err := validateProviderObservationText(versionID, "version ID"); err != nil {
		return err
	}
	if strings.EqualFold(versionID, "null") || strings.EqualFold(versionID, "latest") {
		return fmt.Errorf("%w: version ID must identify an exact immutable version", ErrProviderVersion)
	}
	return nil
}

func validateProviderObservationText(value, label string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxProviderObservationText || !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not canonical", ErrProviderVersion, label)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %s contains control characters", ErrProviderVersion, label)
		}
	}
	return nil
}

// ProviderVersionObservationSet is a bounded handoff helper for a single
// capture. It proves exact retries are idempotent and conflicting observations
// cannot replace an accepted provider version. It is not durable storage and
// does not create a recovery manifest.
type ProviderVersionObservationSet struct {
	mu       sync.RWMutex
	profiles map[string]ProviderProfileIdentity
	objects  map[string]ProviderVersionObservation
}

func (s *ProviderVersionObservationSet) Add(observation ProviderVersionObservation) error {
	if err := ValidateProviderVersionObservation(observation); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.profiles == nil {
		s.profiles = make(map[string]ProviderProfileIdentity)
		s.objects = make(map[string]ProviderVersionObservation)
	}
	if existing, ok := s.profiles[observation.Profile.ProfileID]; ok && existing != observation.Profile {
		return fmt.Errorf("%w: provider profile identity resolves to different configuration", ErrObservationConflict)
	}
	s.profiles[observation.Profile.ProfileID] = observation.Profile
	key := providerObservationObjectIdentity(observation)
	if existing, ok := s.objects[key]; ok {
		if existing != observation {
			return fmt.Errorf("%w: provider object identity already has different immutable metadata", ErrObservationConflict)
		}
		return nil
	}
	if len(s.objects) >= MaxProviderVersionObservations {
		return fmt.Errorf("%w: observation count exceeds %d", ErrProviderVersion, MaxProviderVersionObservations)
	}
	s.objects[key] = observation
	return nil
}

func (s *ProviderVersionObservationSet) Snapshot() []ProviderVersionObservation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]ProviderVersionObservation, 0, len(s.objects))
	for _, observation := range s.objects {
		result = append(result, observation)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := providerObservationObjectIdentity(result[i]), providerObservationObjectIdentity(result[j])
		return left < right
	})
	return result
}

func providerObservationObjectIdentity(observation ProviderVersionObservation) string {
	p := observation.Profile
	return p.ProfileID + "\x00" + observation.ObjectKey
}
