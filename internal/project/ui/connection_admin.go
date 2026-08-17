package ui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectview "github.com/flidai/leapview/internal/project"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

type ConnectionCommandBindings struct {
	Create  uicommand.Binding
	Update  uicommand.Binding
	Test    uicommand.Binding
	Refresh uicommand.Binding
	Enable  uicommand.Binding
	Disable uicommand.Binding
}

type ConnectionBindingView struct {
	ID                    string
	LogicalConnection     string
	ConnectorKind         string
	AuthenticationMode    string
	Host                  string
	Port                  int
	Database              string
	ObjectScope           string
	SourceIdentity        string
	TLSMode               string
	Options               map[string]string
	CredentialProjectID   string
	CredentialEnvironment string
	SecretPath            string
	SecretKey             string
	Enabled               bool
	Health                string
	DiagnosticCode        string
	ValidatedVersion      string
	LastValidatedAt       string
	Revision              int64
}

type ConnectionAdministrationView struct {
	Bindings        map[string]ConnectionBindingView
	CanManage       bool
	CanTest         bool
	RequiresBinding map[string]bool
	Status          uisignals.ConnectionAdministrationStatusSignal
}

func emptyConnectionAdministrationSignal(status uisignals.ConnectionAdministrationStatusSignal) uisignals.ConnectionAdministrationSignal {
	return uisignals.ConnectionAdministrationSignal{
		Command: uisignals.ConnectionAdministrationCommandSignal{},
		Status:  status,
	}
}

func connectionLifecycleSignal(asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, administration ConnectionAdministrationView) uisignals.ConnectionLifecycleSignal {
	logical := ConnectionLogicalName(asset, assets, edges)
	kind := firstNonEmpty(metaString(asset.Payload, "Kind", "kind"), metaString(asset.Payload, "Provider", "provider"))
	requiresBinding, classified := administration.RequiresBinding[logical]
	binding, exists := administration.Bindings[logical]
	lifecycle := uisignals.ConnectionLifecycleSignal{
		Actions:           []uisignals.ConnectionLifecycleActionSignal{},
		AssetID:           asset.ID,
		CanManage:         administration.CanManage,
		CanTest:           administration.CanTest,
		ConnectorKind:     kind,
		LogicalConnection: logical,
		State:             "not_required",
		StatusLabel:       "Not required",
		Tone:              "neutral",
	}
	if !classified {
		// A graph-only browser request has no binding administration snapshot.
		// Anonymous connections are still valid when the compiled definition
		// explicitly says credentials are not required; do not mislabel them as
		// missing configuration merely because no binding row exists.
		if _, hasRequired := asset.Payload["credentials_required"]; hasRequired && !metaBool(asset.Payload, "credentials_required") {
			return lifecycle
		}
		lifecycle.State = "missing"
		lifecycle.StatusLabel = "Not configured"
		lifecycle.Tone = "warning"
		if metaBool(asset.Payload, "credentials_configured") {
			lifecycle.State = "healthy"
			lifecycle.StatusLabel = "Configured"
			lifecycle.Tone = "success"
		}
		return lifecycle
	}
	if !requiresBinding {
		return lifecycle
	}
	if !exists {
		lifecycle.State = "missing"
		lifecycle.StatusLabel = "Not configured"
		lifecycle.Tone = "warning"
		if administration.CanManage {
			lifecycle.Actions = append(lifecycle.Actions, lifecycleAction("configure", "Configure", true, false))
		}
		return lifecycle
	}
	lifecycle.Exists = true
	lifecycle.BindingID = binding.ID
	lifecycle.ConnectorKind = firstNonEmpty(binding.ConnectorKind, kind)
	lifecycle.AuthenticationMode = binding.AuthenticationMode
	lifecycle.Host = binding.Host
	if binding.Port > 0 {
		lifecycle.Port = strconv.Itoa(binding.Port)
	}
	lifecycle.Database = binding.Database
	lifecycle.ObjectScope = binding.ObjectScope
	lifecycle.SourceIdentity = binding.SourceIdentity
	lifecycle.TLSMode = binding.TLSMode
	lifecycle.Options = encodeConnectionOptions(binding.Options)
	lifecycle.CredentialProjectID = binding.CredentialProjectID
	lifecycle.CredentialEnvironment = binding.CredentialEnvironment
	lifecycle.SecretPath = binding.SecretPath
	lifecycle.SecretKey = binding.SecretKey
	lifecycle.Enabled = binding.Enabled
	lifecycle.Health = binding.Health
	lifecycle.DiagnosticCode = binding.DiagnosticCode
	lifecycle.ValidatedVersion = binding.ValidatedVersion
	lifecycle.LastValidatedAt = binding.LastValidatedAt
	lifecycle.Revision = binding.Revision
	if !binding.Enabled || binding.Health == "disabled" {
		lifecycle.State = "disabled"
		lifecycle.StatusLabel = "Disabled"
		lifecycle.Tone = "neutral"
		if administration.CanManage {
			lifecycle.Actions = append(lifecycle.Actions, lifecycleAction("enable", "Enable", true, false))
		}
		return lifecycle
	}
	switch binding.Health {
	case "healthy":
		lifecycle.State = "healthy"
		lifecycle.StatusLabel = "Healthy"
		lifecycle.Tone = "success"
		if administration.CanTest {
			lifecycle.Actions = append(lifecycle.Actions, lifecycleAction("refresh", "Refresh credentials", true, false))
		}
		appendEnabledSecondaryActions(&lifecycle, administration, true)
	case "degraded":
		lifecycle.State = "degraded"
		lifecycle.StatusLabel = "Degraded"
		lifecycle.Tone = "danger"
		if administration.CanTest {
			lifecycle.Actions = append(lifecycle.Actions, lifecycleAction("refresh", "Refresh credentials", true, false))
		}
		appendEnabledSecondaryActions(&lifecycle, administration, true)
	default:
		lifecycle.State = "pending"
		lifecycle.StatusLabel = "Pending test"
		lifecycle.Tone = "warning"
		if administration.CanTest {
			lifecycle.Actions = append(lifecycle.Actions, lifecycleAction("test", "Test connection", true, false))
		}
		appendEnabledSecondaryActions(&lifecycle, administration, false)
	}
	return lifecycle
}

func ConnectionLifecycleForAsset(asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, administration ConnectionAdministrationView) uisignals.ConnectionLifecycleSignal {
	return connectionLifecycleSignal(asset, assets, edges, administration)
}

func appendEnabledSecondaryActions(lifecycle *uisignals.ConnectionLifecycleSignal, administration ConnectionAdministrationView, includeTest bool) {
	if includeTest && administration.CanTest {
		lifecycle.Actions = append(lifecycle.Actions, lifecycleAction("test", "Test connection", false, false))
	}
	if administration.CanManage {
		lifecycle.Actions = append(lifecycle.Actions,
			lifecycleAction("edit", "Edit", false, false),
			lifecycleAction("disable", "Disable", false, true),
		)
	}
}

func lifecycleAction(id, label string, primary, destructive bool) uisignals.ConnectionLifecycleActionSignal {
	return uisignals.ConnectionLifecycleActionSignal{ID: id, Label: label, Primary: primary, Destructive: destructive}
}

func ConnectionLogicalName(connection projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) string {
	for _, source := range sourcesUsingConnection(connection.ID, assets, edges) {
		if value := strings.TrimSpace(metaString(source.Payload, "Connection", "connection")); value != "" {
			return value
		}
	}
	if value := strings.TrimPrefix(strings.TrimSpace(connection.ID), "connection:"); value != "" {
		return value
	}
	return strings.TrimSpace(connection.Key)
}

func encodeConnectionOptions(options map[string]string) string {
	if len(options) == 0 {
		return ""
	}
	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := make(map[string]string, len(options))
	for _, key := range keys {
		ordered[key] = options[key]
	}
	encoded, err := json.Marshal(ordered)
	if err != nil {
		return fmt.Sprint(options)
	}
	return string(encoded)
}
