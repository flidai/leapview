package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access/trustedclaims"
	"github.com/flidai/leapview/internal/semanticvalue"
)

var ErrSemanticAttributeClaimEvidenceInvalid = errors.New("semantic attribute trusted-claim evidence is invalid")

type semanticAttributeClaimValueIdentity struct {
	DefinitionID      string `json:"definitionId"`
	DefinitionVersion int64  `json:"definitionVersion"`
	ValueDigest       string `json:"valueDigest"`
	Source            string `json:"source"`
}

// SemanticAttributeClaimEvidence is an immutable binding between a verified
// authentication envelope, the exact FAI-637 control snapshot used to map it,
// and the claim-derived effective values. It exposes identity only, never raw
// claims or canonical values.
type SemanticAttributeClaimEvidence struct {
	instanceID  string
	principalID string
	control     SemanticAttributeControlState
	evaluatedAt time.Time
	notBefore   time.Time
	notAfter    time.Time
	digest      string
	values      []semanticAttributeClaimValueIdentity
}

func (evidence SemanticAttributeClaimEvidence) Digest() string { return evidence.digest }

func (evidence SemanticAttributeClaimEvidence) Matches(instanceID, principalID string, control SemanticAttributeControlState, attributes []EffectiveSemanticAttribute) bool {
	if evidence.digest == "" || evidence.instanceID != instanceID || evidence.principalID != principalID || evidence.control != control {
		return false
	}
	values := semanticAttributeClaimValueIdentities(attributes)
	return reflect.DeepEqual(values, evidence.values)
}

func (evidence SemanticAttributeClaimEvidence) ValidAt(now time.Time) bool {
	return evidence.digest != "" && !now.IsZero() && now.Equal(now.UTC()) && !now.Before(evidence.evaluatedAt) &&
		!now.Before(evidence.notBefore) && now.Before(evidence.notAfter)
}

// NewSemanticAttributeClaimEvidence validates and seals claim-derived values.
// The control snapshot must be one returned by the verified FAI-637 reader;
// its canonical digest is recomputed here before any mapping is trusted.
func NewSemanticAttributeClaimEvidence(instanceID, principalID string, control SemanticAttributeControlSnapshot, attributes []EffectiveSemanticAttribute, envelope trustedclaims.Envelope, evaluatedAt time.Time) (SemanticAttributeClaimEvidence, error) {
	if strings.TrimSpace(instanceID) != instanceID || instanceID == "" || strings.TrimSpace(principalID) != principalID || principalID == "" {
		return SemanticAttributeClaimEvidence{}, fmt.Errorf("%w: target and principal identity are required", ErrSemanticAttributeClaimEvidenceInvalid)
	}
	if err := ValidateSemanticAttributeControlSnapshot(control); err != nil {
		return SemanticAttributeClaimEvidence{}, fmt.Errorf("%w: control snapshot identity does not match its contents", ErrSemanticAttributeClaimEvidenceInvalid)
	}
	if !envelope.Valid() || envelope.Subject() != principalID || evaluatedAt.IsZero() || !evaluatedAt.Equal(evaluatedAt.UTC()) ||
		evaluatedAt.Before(envelope.NotBefore()) || !evaluatedAt.Before(envelope.NotAfter()) {
		return SemanticAttributeClaimEvidence{}, fmt.Errorf("%w: verified envelope identity or validity is inconsistent", ErrSemanticAttributeClaimEvidenceInvalid)
	}
	values := semanticAttributeClaimValueIdentities(attributes)
	if len(values) == 0 {
		return SemanticAttributeClaimEvidence{}, fmt.Errorf("%w: no claim-derived effective values", ErrSemanticAttributeClaimEvidenceInvalid)
	}
	mappingIDs := make([]string, 0, len(values))
	for _, attribute := range attributes {
		if attribute.Source != "trusted_claim" && attribute.Source != "direct+trusted_claim" {
			continue
		}
		matched := false
		for _, mapping := range control.Mappings {
			if mapping.Tombstoned || string(mapping.SourceKind) != string(envelope.Source()) || mapping.Provider != envelope.Provider() ||
				mapping.Issuer != envelope.Issuer() || mapping.Audience != envelope.Audience() || mapping.DefinitionID != attribute.DefinitionID ||
				mapping.DefinitionName != attribute.DefinitionName || mapping.DefinitionVersion <= 0 || mapping.DefinitionVersion > attribute.DefinitionVersion ||
				mapping.Type != attribute.Type || mapping.Shape != attribute.Shape {
				continue
			}
			raw, found := envelope.Value(mapping.Claim)
			if !found {
				continue
			}
			definition := SemanticAttributeDefinition{ID: attribute.DefinitionID, Name: attribute.DefinitionName, Type: attribute.Type,
				Shape: attribute.Shape, Profile: semanticvalue.Profile, DefinitionVersion: attribute.DefinitionVersion,
				LifecycleState: SemanticAttributeActive, Enabled: true}
			canonical, digest, valueErr := CanonicalSemanticAttributeValues(definition, raw)
			if valueErr != nil || digest != attribute.ValueDigest || !reflect.DeepEqual(canonical, attribute.CanonicalValues) {
				return SemanticAttributeClaimEvidence{}, fmt.Errorf("%w: mapped claim does not match effective attribute %q", ErrSemanticAttributeClaimEvidenceInvalid, attribute.DefinitionName)
			}
			matched = true
			mappingIDs = append(mappingIDs, fmt.Sprintf("%s@%d", mapping.ID, mapping.MappingVersion))
		}
		if !matched {
			return SemanticAttributeClaimEvidence{}, fmt.Errorf("%w: no active exact mapping for effective attribute %q", ErrSemanticAttributeClaimEvidenceInvalid, attribute.DefinitionName)
		}
	}
	sort.Strings(mappingIDs)
	wire := struct {
		Profile               string                                `json:"profile"`
		InstanceID            string                                `json:"instanceId"`
		PrincipalID           string                                `json:"principalId"`
		ControlRevision       int64                                 `json:"controlRevision"`
		ControlDigest         string                                `json:"controlDigest"`
		Source                trustedclaims.SourceKind              `json:"source"`
		Provider              string                                `json:"provider"`
		Issuer                string                                `json:"issuer"`
		Audience              string                                `json:"audience"`
		NotBefore             string                                `json:"notBefore"`
		NotAfter              string                                `json:"notAfter"`
		EvaluatedAt           string                                `json:"evaluatedAt"`
		CredentialFingerprint string                                `json:"credentialFingerprint,omitempty"`
		TokenFingerprint      string                                `json:"tokenFingerprint,omitempty"`
		Mappings              []string                              `json:"mappings"`
		Values                []semanticAttributeClaimValueIdentity `json:"values"`
	}{Profile: semanticvalue.Profile, InstanceID: instanceID, PrincipalID: principalID, ControlRevision: control.State.Revision,
		ControlDigest: control.State.Digest, Source: envelope.Source(), Provider: envelope.Provider(), Issuer: envelope.Issuer(),
		Audience: envelope.Audience(), NotBefore: envelope.NotBefore().UTC().Format(time.RFC3339Nano),
		NotAfter: envelope.NotAfter().UTC().Format(time.RFC3339Nano), EvaluatedAt: evaluatedAt.Format(time.RFC3339Nano),
		CredentialFingerprint: envelope.CredentialFingerprint(), TokenFingerprint: envelope.TokenFingerprint(), Mappings: mappingIDs, Values: values}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return SemanticAttributeClaimEvidence{}, fmt.Errorf("%w: encode identity: %v", ErrSemanticAttributeClaimEvidenceInvalid, err)
	}
	digest, err := semanticAttributeDigestJSON(json.RawMessage(encoded), "claim evidence")
	if err != nil {
		return SemanticAttributeClaimEvidence{}, err
	}
	return SemanticAttributeClaimEvidence{instanceID: instanceID, principalID: principalID, control: control.State, evaluatedAt: evaluatedAt,
		notBefore: envelope.NotBefore().UTC(), notAfter: envelope.NotAfter().UTC(), digest: digest, values: values}, nil
}

func semanticAttributeClaimValueIdentities(attributes []EffectiveSemanticAttribute) []semanticAttributeClaimValueIdentity {
	values := make([]semanticAttributeClaimValueIdentity, 0, len(attributes))
	for _, attribute := range attributes {
		if attribute.Source == "trusted_claim" || attribute.Source == "direct+trusted_claim" {
			values = append(values, semanticAttributeClaimValueIdentity{DefinitionID: attribute.DefinitionID,
				DefinitionVersion: attribute.DefinitionVersion, ValueDigest: attribute.ValueDigest, Source: attribute.Source})
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].DefinitionID < values[j].DefinitionID })
	return values
}
