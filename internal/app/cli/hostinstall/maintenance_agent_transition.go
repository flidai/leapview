package hostinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"net/url"
	"regexp"
	"strings"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/platform/outbound"
	"github.com/google/uuid"
)

var errAgentTransition = errors.New("agent credential transition is invalid or unavailable")
var agentTransitionReference = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type AgentProviderSettings struct {
	Enabled         bool   `json:"enabled"`
	Model           string `json:"model"`
	BaseURL         string `json:"baseUrl"`
	APIMode         string `json:"apiMode"`
	ReasoningEffort string `json:"reasoningEffort"`
}

// This intent belongs only to the private native request. FileDigest must never
// enter public plan reports, runtime records, logs, or workflow artifacts.
type AgentCredentialTransition struct {
	Reference         string                `json:"reference"`
	TransitionVersion string                `json:"transitionVersion"`
	FileDigest        string                `json:"fileDigest"`
	InstallationID    string                `json:"installationId"`
	InstanceID        string                `json:"instanceId"`
	CustomerOwnerID   string                `json:"customerOwnerId"`
	ExpectedRevision  int64                 `json:"expectedRevision"`
	ActorID           string                `json:"actorId"`
	Provider          AgentProviderSettings `json:"provider"`
	ProviderAddress   string                `json:"providerAddress,omitempty"`
}

// Custodian-owned input is retained by default. No lifecycle cleanup deletes
// the only plaintext source; a custodian may withdraw it explicitly.
type AgentTransitionFile struct {
	Version           int                   `json:"version"`
	TransitionVersion string                `json:"transitionVersion"`
	Nonce             string                `json:"nonce"`
	InstallationID    string                `json:"installationId"`
	InstanceID        string                `json:"instanceId"`
	CustomerOwnerID   string                `json:"customerOwnerId"`
	ExpectedRevision  int64                 `json:"expectedRevision"`
	ActorID           string                `json:"actorId"`
	Provider          AgentProviderSettings `json:"provider"`
	LoginEmail        string                `json:"loginEmail"`
	AdminPassword     string                `json:"adminPassword"`
	APIKey            string                `json:"apiKey"`
}

func (i AgentCredentialTransition) validate(target string) error {
	id, err := uuid.Parse(i.TransitionVersion)
	u, urlErr := url.Parse(i.Provider.BaseURL)
	if !agentTransitionReference.MatchString(i.Reference) || err != nil || id == uuid.Nil || id.String() != i.TransitionVersion || !digestPattern.MatchString(i.FileDigest) || i.InstallationID != target || i.ExpectedRevision < 1 || i.ExpectedRevision >= 9007199254740991 || !agentTransitionValue(i.InstallationID) || !agentTransitionValue(i.InstanceID) || !agentTransitionValue(i.CustomerOwnerID) || !agentTransitionValue(i.ActorID) || !i.Provider.Enabled || !agentBoundedTransitionValue(i.Provider.Model, 256) || (i.Provider.APIMode != "responses" && i.Provider.APIMode != "chat-completions") || urlErr != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || (u.Port() != "" && u.Port() != "443") || u.Fragment != "" || u.RawQuery != "" {
		return errAgentTransition
	}
	config := agent.Config{Model: i.Provider.Model, BaseURL: i.Provider.BaseURL, APIMode: i.Provider.APIMode, ReasoningEffort: i.Provider.ReasoningEffort, APIKey: "admission-placeholder"}
	if config.Validate(true) != nil || config.NormalizedBaseURL() != i.Provider.BaseURL || config.NormalizedReasoningEffort() != i.Provider.ReasoningEffort {
		return errAgentTransition
	}
	if config.APIMode == "chat-completions" {
		if strings.HasPrefix(strings.ToLower(config.Model), "deepseek-v4") {
			if config.ReasoningEffort != "none" {
				return errAgentTransition
			}
		} else if config.ReasoningEffort != "" {
			return errAgentTransition
		}
	}
	if i.ProviderAddress != "" && !agentPublicAddress(i.ProviderAddress) {
		return errAgentTransition
	}
	if _, err := netip.ParseAddr(u.Hostname()); err == nil {
		return errAgentTransition
	}
	if i.Provider.BaseURL != strings.TrimSpace(i.Provider.BaseURL) || strings.ContainsAny(u.Hostname(), " \r\n\t") {
		return errAgentTransition
	}
	return nil
}
func agentPublicAddress(raw string) bool {
	ip, err := netip.ParseAddr(raw)
	if err != nil || !ip.Is4() || ip.String() != raw {
		return false
	}
	return outbound.New(outbound.PublicOnly, outbound.Options{}).ValidateHost(context.Background(), raw) == nil
}

func agentTransitionValue(value string) bool { return agentBoundedTransitionValue(value, 255) }
func agentBoundedTransitionValue(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\x00\r\n\t")
}
func ReadAgentTransitionIntent(root, reference string) (AgentCredentialTransition, error) {
	_, intent, err := readAgentTransition(root, reference)
	return intent, err
}
func readBoundAgentTransition(root string, expected AgentCredentialTransition) (AgentTransitionFile, error) {
	file, actual, err := readAgentTransition(root, expected.Reference)
	actual.ProviderAddress = expected.ProviderAddress
	if err != nil || actual != expected || !agentPublicAddress(expected.ProviderAddress) {
		return AgentTransitionFile{}, errAgentTransition
	}
	return file, nil
}
func readAgentTransition(root, reference string) (AgentTransitionFile, AgentCredentialTransition, error) {
	var file AgentTransitionFile
	if !agentTransitionReference.MatchString(reference) {
		return file, AgentCredentialTransition{}, errAgentTransition
	}
	raw, err := readPrivateAgentFile(root, "agent-credential-transitions", reference+".json")
	if err != nil {
		return file, AgentCredentialTransition{}, errAgentTransition
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&file); err != nil {
		return file, AgentCredentialTransition{}, errAgentTransition
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return file, AgentCredentialTransition{}, errAgentTransition
	}
	digest := sha256.Sum256(raw)
	intent := AgentCredentialTransition{Reference: reference, TransitionVersion: file.TransitionVersion, FileDigest: "sha256:" + hex.EncodeToString(digest[:]), InstallationID: file.InstallationID, InstanceID: file.InstanceID, CustomerOwnerID: file.CustomerOwnerID, ExpectedRevision: file.ExpectedRevision, ActorID: file.ActorID, Provider: file.Provider}
	nonce, nonceErr := hex.DecodeString(file.Nonce)
	if file.Version != 1 || nonceErr != nil || len(nonce) != 32 || len(file.APIKey) == 0 || len(file.APIKey) > 16384 || strings.TrimSpace(file.APIKey) != file.APIKey || strings.ContainsAny(file.APIKey, "\x00\r\n\t") || file.AdminPassword == "" || len(file.AdminPassword) > 1024 || !agentTransitionValue(file.LoginEmail) || intent.validate(file.InstallationID) != nil {
		return AgentTransitionFile{}, AgentCredentialTransition{}, errAgentTransition
	}
	return file, intent, nil
}

func agentTransitionOutput(file AgentTransitionFile, intent AgentCredentialTransition, operation, candidateRevision string, stdout io.Writer) error {
	// Dedicated private IPC only: this command is wired directly to the browser
	// child's stdin and must never be run with a terminal or artifact writer.
	if !sourceRevisionPattern.MatchString(candidateRevision) {
		return errAgentTransition
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"intent": intent, "operationDigest": operation, "candidateRevision": candidateRevision, "loginEmail": file.LoginEmail, "adminPassword": file.AdminPassword, "apiKey": file.APIKey})
}
