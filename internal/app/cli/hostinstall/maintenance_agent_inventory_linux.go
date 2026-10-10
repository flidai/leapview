//go:build linux

package hostinstall

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/platform/outbound"
)

// Schema 58 lacks credential_version_id. Only bounded, explicitly allowlisted
// metadata leaves PostgreSQL; ciphertext and whole configuration never do.
const agentTransitionInventorySQL = `SELECT json_build_object(
 'instanceId', o.instance_id, 'customerOwnerId', o.owner_id,
 'revision', c.revision, 'enabled', c.enabled,
 'legacy', octet_length(c.credential) > 0,
 'model', c.config_json->>'Model', 'baseUrl', c.config_json->>'BaseURL',
 'apiMode', c.config_json->>'APIMode', 'reasoningEffort', c.config_json->>'ReasoningEffort')
 FROM agent.configuration_revisions c
 CROSS JOIN platform.instance_customer_owner o WHERE o.singleton_id = 1
 ORDER BY c.revision DESC LIMIT 1`

func validateAgentInventory(raw string, intent AgentCredentialTransition) error {
	var row struct {
		InstanceID      string `json:"instanceId"`
		CustomerOwnerID string `json:"customerOwnerId"`
		Revision        int64  `json:"revision"`
		Legacy          bool   `json:"legacy"`
		AgentProviderSettings
	}
	if len(raw) > 4096 || json.Unmarshal([]byte(raw), &row) != nil || row.InstanceID != intent.InstanceID || row.CustomerOwnerID != intent.CustomerOwnerID || row.Revision != intent.ExpectedRevision || !row.Legacy {
		return errAgentTransition
	}
	config := agent.Config{Model: row.Model, BaseURL: row.BaseURL, APIMode: row.APIMode, ReasoningEffort: row.ReasoningEffort}
	// This deployment recovery replaces the key while retaining provider settings.
	// Endpoint/model changes require their own ordinary administrator operation.
	settings := AgentProviderSettings{Enabled: row.Enabled, Model: strings.TrimSpace(config.Model), BaseURL: config.NormalizedBaseURL(), APIMode: strings.TrimSpace(config.APIMode), ReasoningEffort: config.NormalizedReasoningEffort()}
	desired := intent.Provider
	desired.BaseURL = strings.TrimRight(desired.BaseURL, "/")
	if settings != desired {
		return errAgentTransition
	}
	return nil
}

// Called only before a fresh live capture. Recovery and terminal plans use the
// original immutable request/journal, independent of mutable candidate rows.
func (e *NativeEffects) validateAgentTransitionInventory(ctx context.Context) error {
	intent := e.request.AgentCredentialTransition
	if intent == nil {
		return nil
	}
	if e.request.Plan.CurrentSchema < 58 {
		return errAgentTransition
	}
	raw, err := e.pgQuery(ctx, e.request.Profile.Postgres, "leapview_control", agentTransitionInventorySQL)
	if err != nil || validateAgentInventory(raw, *intent) != nil {
		return errAgentTransition
	}
	file, err := readBoundAgentTransition(e.request.Profile.StateRoot, *intent)
	if err != nil {
		return err
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	query := `SELECT EXISTS (SELECT 1 FROM access.principal p JOIN access.platform_role_binding b ON b.principal_id=p.id WHERE p.id::text=` + quote(intent.ActorID) + ` AND lower(p.email)=lower(` + quote(file.LoginEmail) + `) AND p.principal_type='user' AND p.status='active' AND p.revoked_at IS NULL AND p.disabled_at IS NULL AND p.blocked_at IS NULL AND b.role='platform_admin' AND b.revoked_at IS NULL)`
	allowed, err := e.pgQuery(ctx, e.request.Profile.Postgres, "leapview_control", query)
	if err != nil || allowed != "t" {
		return errAgentTransition
	}
	return nil
}

func runAgentTransitionCommand(ctx context.Context, action string, r NativeRequest, reference string, stdout io.Writer) error {
	if err := checkMaintenancePlan(ctx, r); err != nil {
		return errAgentTransition
	}
	if !privateAgentExportWriter(stdout) {
		return errAgentTransition
	}
	if action == "agent-intent" {
		intent, err := ReadAgentTransitionIntent(r.Profile.StateRoot, reference)
		if err != nil || intent.validate(r.Profile.ID) != nil {
			return errAgentTransition
		}
		u, _ := url.Parse(intent.Provider.BaseURL)
		addresses, err := outbound.New(outbound.PublicOnly, outbound.Options{}).ResolveHost(ctx, u.Hostname())
		if err != nil {
			return errAgentTransition
		}
		sort.Slice(addresses, func(a, b int) bool { return addresses[a].Less(addresses[b]) })
		for _, address := range addresses {
			if address.Is4() {
				intent.ProviderAddress = address.String()
				break
			}
		}
		if !agentPublicAddress(intent.ProviderAddress) {
			return errAgentTransition
		}
		r.AgentCredentialTransition = &intent
		e := &NativeEffects{request: r}
		if err = e.validateAgentTransitionInventory(ctx); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(intent)
	}
	if action != "agent-export" || r.AgentCredentialTransition == nil {
		return errAgentTransition
	}
	id, err := r.Identity()
	if err != nil {
		return errAgentTransition
	}
	if err = validateAgentExportPhase(r, id); err != nil {
		return err
	}
	file, err := readBoundAgentTransition(r.Profile.StateRoot, *r.AgentCredentialTransition)
	if err != nil {
		return err
	}
	return agentTransitionOutput(file, *r.AgentCredentialTransition, id.ArtifactAdmissionDigest, stdout)
}
