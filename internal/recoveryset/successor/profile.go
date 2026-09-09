package successor

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// ProviderProfileSet is trusted configuration selected independently from a
// submitted observation manifest. Account identity is opaque and is never
// derived from an endpoint or credential.
type ProviderProfileSet struct {
	ProfileVersion int32             `json:"profile_version"`
	Profiles       []ProviderProfile `json:"profiles"`
}

type ProviderProfile struct {
	Implementation   string      `json:"implementation"`
	AccountIdentity  string      `json:"account_identity"`
	Endpoint         string      `json:"endpoint"`
	Region           string      `json:"region"`
	Bucket           string      `json:"bucket"`
	VersionSemantics string      `json:"version_semantics"`
	Namespaces       []Namespace `json:"namespaces"`
}

type Namespace struct {
	ProjectID    string `json:"project_id"`
	CollectionID string `json:"collection_id"`
	Prefix       string `json:"prefix"`
}

func (p ProviderProfileSet) Validate() error {
	if p.ProfileVersion != ProviderProfileVersion {
		return fmt.Errorf("%w: provider profile version %d", ErrUnsupportedVersion, p.ProfileVersion)
	}
	if p.Profiles == nil || len(p.Profiles) > MaxProfiles {
		return invalid("profiles must be an explicit bounded array")
	}
	seenPhysical := map[string]struct{}{}
	namespaceCount := 0
	for i, profile := range p.Profiles {
		if profile.Implementation != "s3" || profile.VersionSemantics != "opaque-exact-version" {
			return invalid("profile %d has unsupported implementation or version semantics", i)
		}
		if err := id(profile.AccountIdentity, "profile account_identity"); err != nil {
			return err
		}
		if err := id(profile.Region, "profile region"); err != nil {
			return err
		}
		if !validBucket(profile.Bucket) {
			return invalid("profile %d bucket is invalid", i)
		}
		endpoint, err := canonicalEndpoint(profile.Endpoint)
		if err != nil {
			return err
		}
		if endpoint != profile.Endpoint {
			return invalid("profile endpoint is not canonical")
		}
		physical := profile.Implementation + "\x00" + profile.AccountIdentity + "\x00" + profile.Endpoint + "\x00" + profile.Bucket
		if _, exists := seenPhysical[physical]; exists {
			return invalid("duplicate provider profile physical bucket")
		}
		seenPhysical[physical] = struct{}{}
		if profile.Namespaces == nil || len(profile.Namespaces) == 0 {
			return invalid("profile namespaces must be an explicit nonempty array")
		}
		seenNamespace := map[string]struct{}{}
		for _, namespace := range profile.Namespaces {
			if err := id(namespace.ProjectID, "namespace project_id"); err != nil {
				return err
			}
			if err := id(namespace.CollectionID, "namespace collection_id"); err != nil {
				return err
			}
			if namespace.Prefix != "" {
				if err := text(namespace.Prefix, "namespace prefix", MaxTextBytes); err != nil {
					return err
				}
				if strings.HasSuffix(namespace.Prefix, "/") {
					return invalid("namespace prefix must not end in slash")
				}
			}
			key := namespace.ProjectID + "\x00" + namespace.CollectionID + "\x00" + namespace.Prefix
			if _, exists := seenNamespace[key]; exists {
				return invalid("duplicate provider namespace")
			}
			seenNamespace[key] = struct{}{}
			namespaceCount++
			if namespaceCount > MaxNamespaces {
				return invalid("provider namespace limit exceeded")
			}
		}
	}
	return nil
}

func (p ProviderProfileSet) Normalize() (ProviderProfileSet, error) {
	if err := p.Validate(); err != nil {
		return ProviderProfileSet{}, err
	}
	normalized := p
	normalized.Profiles = make([]ProviderProfile, len(p.Profiles))
	copy(normalized.Profiles, p.Profiles)
	for i := range normalized.Profiles {
		normalized.Profiles[i].Namespaces = make([]Namespace, len(p.Profiles[i].Namespaces))
		copy(normalized.Profiles[i].Namespaces, p.Profiles[i].Namespaces)
		sort.Slice(normalized.Profiles[i].Namespaces, func(a, b int) bool {
			left, right := normalized.Profiles[i].Namespaces[a], normalized.Profiles[i].Namespaces[b]
			if left.ProjectID != right.ProjectID {
				return left.ProjectID < right.ProjectID
			}
			if left.CollectionID != right.CollectionID {
				return left.CollectionID < right.CollectionID
			}
			return left.Prefix < right.Prefix
		})
	}
	sort.Slice(normalized.Profiles, func(a, b int) bool {
		left, right := normalized.Profiles[a], normalized.Profiles[b]
		if left.Implementation != right.Implementation {
			return left.Implementation < right.Implementation
		}
		if left.AccountIdentity != right.AccountIdentity {
			return left.AccountIdentity < right.AccountIdentity
		}
		if left.Endpoint != right.Endpoint {
			return left.Endpoint < right.Endpoint
		}
		return left.Bucket < right.Bucket
	})
	return normalized, nil
}

func (p ProviderProfileSet) CanonicalJSON() ([]byte, error) {
	normalized, err := p.Normalize()
	if err != nil {
		return nil, err
	}
	return marshal(normalized, MaxDocumentBytes)
}
func (p ProviderProfileSet) Digest() (string, error) {
	raw, err := p.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return hash(profileDomain, raw), nil
}

// ValidateManifest binds every provider observation to one trusted profile and
// exact project/collection namespace. A matching digest alone is not trust;
// callers must provide this independently selected profile value.
func (p ProviderProfileSet) ValidateManifest(manifest ManagedManifest2, scope ExpectedScope) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := manifest.ValidateFor(scope); err != nil {
		return err
	}
	if err := manifest.validateObservations(); err != nil {
		return err
	}
	for _, revision := range manifest.Revisions {
		for _, file := range revision.Files {
			matched := false
			for _, profile := range p.Profiles {
				provider := file.Provider
				if provider.Implementation != profile.Implementation || provider.AccountIdentity != profile.AccountIdentity || provider.Endpoint != profile.Endpoint || provider.Region != profile.Region || provider.Bucket != profile.Bucket {
					continue
				}
				for _, namespace := range profile.Namespaces {
					if namespace.ProjectID == revision.ProjectID && namespace.CollectionID == revision.CollectionID && namespace.Prefix == provider.Prefix && (namespace.Prefix == "" || strings.HasPrefix(provider.Key, namespace.Prefix+"/")) {
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			if !matched {
				return invalid("provider observation is not authorized by a selected profile namespace")
			}
		}
	}
	return nil
}

func ParseProviderProfileSet(raw []byte) (ProviderProfileSet, error) {
	var profiles ProviderProfileSet
	if err := strictDecode(raw, &profiles, MaxDocumentBytes); err != nil {
		return profiles, err
	}
	if _, err := exactObject(raw, map[string]bool{"profile_version": true, "profiles": true}, map[string]bool{"profile_version": true, "profiles": true}, "provider profiles"); err != nil {
		return profiles, err
	}
	canonical, err := profiles.CanonicalJSON()
	if err != nil {
		return profiles, err
	}
	if !bytes.Equal(raw, canonical) {
		return profiles, invalid("provider profiles are not canonical JSON")
	}
	return profiles, nil
}
