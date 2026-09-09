package postgres

import (
	"bytes"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/recoveryset/successor"
	"golang.org/x/text/unicode/norm"
)

const successorLocatorLimit = 16 * 1024

// This is the frozen transport document, not a new recovery commitment. The
// in-memory flat locator is encoded with the ordered versioned target envelope.
type successorLocatorTarget struct {
	Backend                string `json:"backend"`
	StorageProfileID       string `json:"storage_profile_id"`
	StorageProfileRevision int64  `json:"storage_profile_revision"`
	AccountIdentity        string `json:"account_identity"`
	Endpoint               string `json:"endpoint"`
	Region                 string `json:"region"`
	Bucket                 string `json:"bucket"`
	Namespace              string `json:"namespace"`
	Key                    string `json:"key"`
}
type successorLocatorWire struct {
	Kind           string                 `json:"kind"`
	Version        int32                  `json:"version"`
	Target         successorLocatorTarget `json:"target"`
	VersionID      string                 `json:"version_id"`
	PayloadFamily  string                 `json:"payload_family"`
	PayloadVersion int32                  `json:"payload_version"`
	PayloadDigest  string                 `json:"payload_digest"`
	PayloadSHA256  string                 `json:"payload_sha256"`
	PayloadSize    int64                  `json:"payload_size"`
}

func (l ValidatedLocator) MarshalJSON() ([]byte, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(l.wire())
	if err != nil {
		return nil, err
	}
	if len(raw) > successorLocatorLimit {
		return nil, ErrSuccessorInvalid
	}
	return raw, nil
}

func (l ValidatedLocator) wire() successorLocatorWire {
	return successorLocatorWire{Kind: "leapview.recovery-evidence-locator", Version: 1,
		Target:    successorLocatorTarget{l.Backend, l.StorageProfileID, l.StorageProfileRevision, l.AccountIdentity, l.Endpoint, l.Region, l.Bucket, l.Namespace, l.Key},
		VersionID: l.VersionID, PayloadFamily: l.PayloadFamily, PayloadVersion: l.PayloadVersion, PayloadDigest: l.PayloadDigest, PayloadSHA256: l.PayloadSHA256, PayloadSize: l.PayloadSize}
}

func (l *ValidatedLocator) UnmarshalJSON(raw []byte) error {
	if l == nil || len(raw) > successorLocatorLimit {
		return ErrSuccessorInvalid
	}
	var w successorLocatorWire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&w); err != nil {
		return ErrSuccessorInvalid
	}
	if w.Kind != "leapview.recovery-evidence-locator" || w.Version != 1 {
		return ErrSuccessorInvalid
	}
	value := ValidatedLocator{Backend: w.Target.Backend, StorageProfileID: w.Target.StorageProfileID, StorageProfileRevision: w.Target.StorageProfileRevision,
		AccountIdentity: w.Target.AccountIdentity, Endpoint: w.Target.Endpoint, Region: w.Target.Region, Bucket: w.Target.Bucket, Namespace: w.Target.Namespace, Key: w.Target.Key,
		VersionID: w.VersionID, PayloadFamily: w.PayloadFamily, PayloadVersion: w.PayloadVersion, PayloadDigest: w.PayloadDigest, PayloadSHA256: w.PayloadSHA256, PayloadSize: w.PayloadSize}
	canonical, err := value.MarshalJSON()
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, canonical) {
		return ErrSuccessorInvalid
	}
	*l = value
	return nil
}

func (l ValidatedLocator) Validate() error {
	if l.Backend != "s3" || !canonicalUUID(l.StorageProfileID) || l.StorageProfileRevision <= 0 {
		return ErrSuccessorInvalid
	}
	for _, value := range []string{l.AccountIdentity, l.Region, l.Bucket, l.Namespace, l.VersionID} {
		if !successorLocatorText(value, 4096) {
			return ErrSuccessorInvalid
		}
	}
	if !successorLocatorText(l.Key, 1024) || !successorLocatorText(l.Endpoint, 2048) || strings.EqualFold(l.VersionID, "latest") || strings.EqualFold(l.VersionID, "null") {
		return ErrSuccessorInvalid
	}
	u, err := url.Parse(l.Endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ToLower(u.Host) != u.Host || u.String() != l.Endpoint || !successorDNSName(u.Hostname()) {
		return ErrSuccessorInvalid
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || n == 443 || strconv.Itoa(n) != port {
			return ErrSuccessorInvalid
		}
	} else if strings.Contains(u.Host, ":") {
		return ErrSuccessorInvalid
	}
	if len(l.Bucket) < 3 || len(l.Bucket) > 63 || !successorDNSName(l.Bucket) {
		return ErrSuccessorInvalid
	}
	for _, segment := range strings.Split(l.Namespace, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, `\%`) {
			return ErrSuccessorInvalid
		}
	}
	version := familyVersion(l.PayloadFamily)
	maxSize := int64(successor.MaxDocumentBytes)
	if l.PayloadFamily == PayloadFamilySet {
		maxSize = successor.MaxSetBytes
	}
	if version == 0 || l.PayloadVersion != version || l.PayloadSize < 2 || l.PayloadSize > maxSize || !rawSHA256.MatchString(l.PayloadSHA256) || !domainDigest.MatchString(l.PayloadDigest) {
		return ErrSuccessorInvalid
	}
	// familyVersion is the fixed reviewed allowlist; no caller path token is used.
	token := strings.TrimPrefix(l.PayloadFamily, "leapview.")
	want := l.Namespace + "/" + token + "/v" + strconv.Itoa(int(version)) + "/sha256/" + l.PayloadSHA256
	if l.Key != want {
		return ErrSuccessorInvalid
	}
	raw, err := json.Marshal(l.wire())
	if err != nil || len(raw) > successorLocatorLimit {
		return ErrSuccessorInvalid
	}
	return nil
}

func successorLocatorText(value string, max int) bool {
	if value == "" || len(value) > max || value != strings.TrimSpace(value) || !utf8.ValidString(value) || !norm.NFC.IsNormalString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return false
		}
	}
	return true
}

func successorDNSName(host string) bool {
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
