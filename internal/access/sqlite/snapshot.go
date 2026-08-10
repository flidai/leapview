package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flidai/leapview/internal/access"
	platformdb "github.com/flidai/leapview/internal/access/internal/db"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/platform/transaction"
)

type snapshotAsset struct {
	assetType   string
	assetKey    string
	payloadJSON string
}

// ApplySnapshotTx installs the authored access policy and the securable
// projection for one serving state inside its caller's activation transaction.
func ApplySnapshotTx(ctx context.Context, tx transaction.Transaction, servingStateID string) error {
	var workspaceID, ownerID, policyJSON string
	if err := tx.QueryRowContext(ctx, `
SELECT workspace_id, created_by, access_policy_json
FROM serving_states WHERE id = ?`, servingStateID).Scan(&workspaceID, &ownerID, &policyJSON); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
SELECT asset_type, asset_key, payload_json
FROM assets WHERE serving_state_id = ?
ORDER BY asset_type, asset_key`, servingStateID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var assets []snapshotAsset
	for rows.Next() {
		var asset snapshotAsset
		if err := rows.Scan(&asset.assetType, &asset.assetKey, &asset.payloadJSON); err != nil {
			return err
		}
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	policy, err := decodeAccessPolicyJSON(policyJSON)
	if err != nil {
		return fmt.Errorf("decode access policy: %w", err)
	}
	repository := &Repository{db: tx, q: platformdb.New(tx)}
	if err := installSecurables(ctx, repository, workspaceID, ownerID, assets); err != nil {
		return err
	}
	return installPolicy(ctx, repository, workspaceID, policy)
}

func decodeAccessPolicyJSON(value string) (accesssnapshot.AccessPolicy, error) {
	return accesssnapshot.Decode([]byte(value))
}

func installSecurables(ctx context.Context, repository *Repository, workspaceID, ownerID string, assets []snapshotAsset) error {
	workspaceObject := access.WorkspaceObject(workspaceID)
	if _, err := repository.UpsertSecurableObject(ctx, workspaceObject, ownerID); err != nil {
		return err
	}
	for _, asset := range assets {
		key := strings.TrimPrefix(strings.TrimSpace(asset.assetKey), strings.TrimSpace(workspaceID)+".")
		var object access.ObjectRef
		switch asset.assetType {
		case "dashboard":
			object = access.ItemObjectWithParent(access.SecurableDashboard, workspaceID, key, workspaceObject)
		case "semantic_model":
			object = access.ItemObjectWithParent(access.SecurableSemanticModel, workspaceID, key, workspaceObject)
		case "source":
			object = access.ItemObjectWithParent(access.SecurableSource, workspaceID, key, workspaceObject)
		case "model_table":
			object = access.ItemObjectWithParent(access.SecurableModelTable, workspaceID, key, workspaceObject)
		case "semantic_table":
			modelID, tableID, ok := strings.Cut(key, ".")
			if !ok {
				continue
			}
			model := access.ItemObjectWithParent(access.SecurableSemanticModel, workspaceID, modelID, workspaceObject)
			object = access.ItemObjectWithParent(access.SecurableDataset, workspaceID, modelID+"/"+tableID, model)
		case "field":
			parts := strings.Split(key, ".")
			if len(parts) != 3 {
				continue
			}
			model := access.ItemObjectWithParent(access.SecurableSemanticModel, workspaceID, parts[0], workspaceObject)
			table := access.ItemObjectWithParent(access.SecurableDataset, workspaceID, parts[0]+"/"+parts[1], model)
			object = access.ItemObjectWithParent(access.SecurableColumn, workspaceID, strings.Join(parts, "/"), table)
		case "measure":
			modelID, memberID, ok := strings.Cut(key, ".")
			if !ok {
				continue
			}
			model := access.ItemObjectWithParent(access.SecurableSemanticModel, workspaceID, modelID, workspaceObject)
			object = access.ItemObjectWithParent(access.SecurableSemanticField, workspaceID, modelID+"/"+memberID, model)
		default:
			continue
		}
		if _, err := repository.UpsertSecurableObject(ctx, object, ownerID); err != nil {
			return err
		}
		if asset.assetType == "semantic_model" {
			if err := installSemanticFields(ctx, repository, workspaceID, ownerID, key, asset.payloadJSON); err != nil {
				return err
			}
		}
	}
	return nil
}

func installSemanticFields(ctx context.Context, repository *Repository, workspaceID, ownerID, modelID, payloadJSON string) error {
	var payload struct {
		Dimensions map[string]json.RawMessage `json:"Dimensions"`
		Measures   map[string]json.RawMessage `json:"Measures"`
		Metrics    map[string]json.RawMessage `json:"Metrics"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return fmt.Errorf("decode semantic model %q payload for securable registration: %w", modelID, err)
	}
	workspaceObject := access.WorkspaceObject(workspaceID)
	model := access.ItemObjectWithParent(access.SecurableSemanticModel, workspaceID, modelID, workspaceObject)
	for _, members := range []map[string]json.RawMessage{payload.Dimensions, payload.Measures, payload.Metrics} {
		for name := range members {
			if _, err := repository.UpsertSecurableObject(ctx, access.ItemObjectWithParent(access.SecurableSemanticField, workspaceID, modelID+"/"+name, model), ownerID); err != nil {
				return err
			}
		}
	}
	return nil
}

func installPolicy(ctx context.Context, repository *Repository, workspaceID string, policy accesssnapshot.AccessPolicy) error {
	bindings, err := repository.ListRoleBindings(ctx, workspaceID)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if err := repository.DeleteRoleBinding(ctx, workspaceID, binding.ID); err != nil {
			return err
		}
	}
	groups, err := repository.ListGroups(ctx, workspaceID)
	if err != nil {
		return err
	}
	for _, group := range groups {
		if err := repository.DeleteGroup(ctx, workspaceID, group.ID); err != nil {
			return err
		}
	}
	if err := repository.q.DeleteWorkspaceGrants(ctx, platformdb.DeleteWorkspaceGrantsParams{
		WorkspaceID: workspaceID, WorkspaceObjectID: access.WorkspaceObject(workspaceID).CanonicalID(),
	}); err != nil {
		return err
	}
	if err := repository.q.DeleteWorkspaceDataPolicies(ctx, workspaceID); err != nil {
		return err
	}
	groupIDs := map[string]string{}
	for _, name := range sortedSnapshotKeys(policy.Groups) {
		group := policy.Groups[name]
		id := stableSnapshotID("group", workspaceID, name)
		if _, err := repository.UpsertGroup(ctx, access.GroupInput{
			ID: id, WorkspaceID: workspaceID, Provider: "local", ExternalID: name, Name: firstSnapshotValue(group.Name, name),
		}); err != nil {
			return err
		}
		groupIDs[name] = id
		for _, member := range group.Members {
			principal, err := repository.UpsertPrincipal(ctx, access.PrincipalInput{
				ID: snapshotPrincipalID(member.PrincipalID, member.Email), Kind: access.PrincipalKindUser, Email: member.Email,
				DisplayName: firstSnapshotValue(member.DisplayName, member.Email, member.PrincipalID),
			})
			if err != nil {
				return err
			}
			if err := repository.AddGroupMember(ctx, workspaceID, id, principal.ID); err != nil {
				return err
			}
		}
	}
	for _, name := range sortedSnapshotKeys(policy.RoleBindings) {
		binding := policy.RoleBindings[name]
		subjectType, subjectID, err := snapshotSubject(ctx, repository, workspaceID, binding.Subject, groupIDs)
		if err != nil {
			return fmt.Errorf("workspace role binding %q: %w", name, err)
		}
		if _, err := repository.CreateRoleBinding(ctx, access.RoleBindingInput{
			ID: stableSnapshotID("rolebinding", workspaceID, name), WorkspaceID: workspaceID,
			SubjectType: subjectType, SubjectID: subjectID, Role: binding.Role,
		}); err != nil {
			return err
		}
	}
	for _, name := range sortedSnapshotKeys(policy.Grants) {
		grant := policy.Grants[name]
		subjectType, subjectID, err := snapshotSubject(ctx, repository, workspaceID, grant.Subject, groupIDs)
		if err != nil {
			return fmt.Errorf("workspace grant %q: %w", name, err)
		}
		if _, err := repository.CreateGrant(ctx, access.GrantInput{
			Object: snapshotObject(workspaceID, grant.Object), SubjectType: subjectType,
			SubjectID: subjectID, Privilege: access.Privilege(grant.Privilege),
		}); err != nil {
			return err
		}
	}
	for _, name := range sortedSnapshotKeys(policy.DataPolicies) {
		item := policy.DataPolicies[name]
		var subjectType access.SubjectType
		var subjectID string
		if item.Subject.Kind != "" {
			subjectType, subjectID, err = snapshotSubject(ctx, repository, workspaceID, item.Subject, groupIDs)
			if err != nil {
				return fmt.Errorf("workspace data policy %q: %w", name, err)
			}
		}
		if _, err := repository.UpsertDataPolicy(ctx, access.DataPolicyInput{
			ID: stableSnapshotID("datapolicy", workspaceID, name), Object: snapshotObject(workspaceID, item.Object),
			SubjectType: subjectType, SubjectID: subjectID, PolicyType: item.PolicyType, ExpressionJSON: item.ExpressionJSON,
		}); err != nil {
			return err
		}
	}
	return nil
}

func snapshotSubject(ctx context.Context, repository *Repository, workspaceID string, subject accesssnapshot.Subject, groupIDs map[string]string) (access.SubjectType, string, error) {
	switch access.SubjectType(subject.Kind) {
	case access.SubjectGroup:
		if groupIDs[subject.Group] == "" {
			return "", "", fmt.Errorf("unknown group %q", subject.Group)
		}
		return access.SubjectGroup, groupIDs[subject.Group], nil
	case access.SubjectPrincipal:
		principal, err := repository.UpsertPrincipal(ctx, access.PrincipalInput{
			ID: snapshotPrincipalID(subject.PrincipalID, subject.Email), Kind: access.PrincipalKindUser, Email: subject.Email,
			DisplayName: firstSnapshotValue(subject.DisplayName, subject.Email, subject.PrincipalID),
		})
		return access.SubjectPrincipal, principal.ID, err
	case access.SubjectServicePrincipal:
		principal, err := repository.UpsertPrincipal(ctx, access.PrincipalInput{
			ID: subject.PrincipalID, Kind: access.PrincipalKindServicePrincipal,
			DisplayName: firstSnapshotValue(subject.DisplayName, subject.PrincipalID),
		})
		return access.SubjectServicePrincipal, principal.ID, err
	case access.SubjectDashboardPublication:
		return access.SubjectDashboardPublication, access.DashboardPublicationSubjectID(workspaceID, subject.Publication), nil
	default:
		return "", "", fmt.Errorf("unsupported subject kind %q", subject.Kind)
	}
}

func snapshotPrincipalID(principalID, email string) string {
	if principalID = strings.TrimSpace(principalID); principalID != "" {
		return principalID
	}
	if email = access.NormalizeEmail(email); email != "" {
		return access.PrincipalIDForEmail(email)
	}
	return ""
}

func snapshotObject(workspaceID string, object accesssnapshot.ObjectRef) access.ObjectRef {
	typ := access.SecurableType(strings.TrimSpace(object.Type))
	id := strings.TrimSpace(object.ID)
	if typ == access.SecurableWorkspace {
		return access.WorkspaceObject(workspaceID)
	}
	return access.ItemObject(typ, workspaceID, id)
}

func sortedSnapshotKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func stableSnapshotID(prefix, workspaceID, name string) string {
	return prefix + "_" + stableID(strings.ToLower(strings.TrimSpace(workspaceID)+"|"+strings.TrimSpace(name)))
}

func firstSnapshotValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
