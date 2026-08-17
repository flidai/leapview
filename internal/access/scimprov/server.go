package scimprov

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	scimpkg "github.com/elimity-com/scim"
	scimerrors "github.com/elimity-com/scim/errors"
	"github.com/elimity-com/scim/optional"
	"github.com/elimity-com/scim/schema"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/platform/security/secret"
	parserfilter "github.com/scim2/filter-parser/v2"
)


type Repository interface {
	UpsertSCIMUser(ctx context.Context, input access.SCIMUserInput) (access.SCIMUser, error)
	ListSCIMUsers(ctx context.Context, filter access.SCIMUserFilter) ([]access.SCIMUser, error)
	DisableSCIMUser(ctx context.Context, principalID string) (access.SCIMUser, error)
	UpsertSCIMGroup(ctx context.Context, input access.SCIMGroupInput) (access.Group, error)
	ListSCIMGroups(ctx context.Context, filter access.SCIMGroupFilter) ([]access.Group, error)
	DeleteSCIMGroup(ctx context.Context, groupID string) error
	AddSCIMGroupMember(ctx context.Context, groupID, principalID string) error
	RemoveSCIMGroupMember(ctx context.Context, groupID, principalID string) error
	ListSCIMGroupMembers(ctx context.Context, groupID string) ([]access.GroupMember, error)
	RecordAuditEvent(ctx context.Context, input access.AuditEventInput) error
}

type Options struct {
	Repository  Repository
	BearerToken string
}

func NewHandler(options Options) (http.Handler, error) {
	if options.Repository == nil {
		return nil, fmt.Errorf("scim repository is required")
	}
	token := strings.TrimSpace(options.BearerToken)
	if token == "" {
		return nil, fmt.Errorf("scim bearer token is required")
	}
	users := userHandler{repo: options.Repository}
	groups := groupHandler{repo: options.Repository}
	server, err := scimpkg.NewServer(&scimpkg.ServerArgs{
		ServiceProviderConfig: &scimpkg.ServiceProviderConfig{
			AuthenticationSchemes: []scimpkg.AuthenticationScheme{{
				Type:        scimpkg.AuthenticationTypeOauthBearerToken,
				Name:        "Bearer",
				Description: "SCIM provisioning bearer token",
				Primary:     true,
			}},
			MaxResults:       200,
			SupportFiltering: true,
			SupportPatch:     true,
		},
		ResourceTypes: []scimpkg.ResourceType{
			{
				ID:          optional.NewString("User"),
				Name:        "User",
				Description: optional.NewString("LeapView principal"),
				Endpoint:    "/Users",
				Schema:      schema.CoreUserSchema(),
				Handler:     users,
			},
			{
				ID:          optional.NewString("Group"),
				Name:        "Group",
				Description: optional.NewString("LeapView directory group"),
				Endpoint:    "/Groups",
				Schema:      schema.CoreGroupSchema(),
				Handler:     groups,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return bearerMiddleware(token, options.Repository, server), nil
}

func bearerMiddleware(expected string, repo Repository, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !secret.Equal(bearerToken(r), expected) {
			recordAudit(r.Context(), repo, r, "scim.auth", "scim", "scim", "denied", errors.New("invalid scim bearer token"))
			w.Header().Set("Content-Type", "application/scim+json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"401","detail":"Unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) string {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return ""
	}
	return fields[1]
}

type userHandler struct {
	repo Repository
}

func (h userHandler) Create(r *http.Request, attrs scimpkg.ResourceAttributes) (scimpkg.Resource, error) {
	input := scimUserInput("", attrs)
	var user access.SCIMUser
	err := runAuditedMutation(r, h.repo, func(txRepo Repository) (access.AuditEventInput, error) {
		var mutationErr error
		user, mutationErr = txRepo.UpsertSCIMUser(r.Context(), input)
		return scimAuditInput(r, "scim.user.create", "principal", user.Principal.ID, "success", nil), mutationErr
	})
	if err != nil {
		recordAudit(r.Context(), h.repo, r, "scim.user.create", "principal", input.ExternalID, "error", err)
		return scimpkg.Resource{}, err
	}
	return userResource(user.Principal, user.ExternalID), nil
}

func (h userHandler) Get(r *http.Request, id string) (scimpkg.Resource, error) {
	users, err := h.repo.ListSCIMUsers(r.Context(), access.SCIMUserFilter{ID: id})
	if err != nil {
		return scimpkg.Resource{}, err
	}
	if len(users) == 0 {
		return scimpkg.Resource{}, scimerrors.ScimErrorResourceNotFound(id)
	}
	return userResource(users[0].Principal, users[0].ExternalID), nil
}

func (h userHandler) GetAll(r *http.Request, params scimpkg.ListRequestParams) (scimpkg.Page, error) {
	filter := access.SCIMUserFilter{}
	if attr, value, ok := eqFilter(params); ok {
		switch strings.ToLower(attr) {
		case "username":
			filter.UserName = value
		case "externalid":
			filter.ExternalID = value
		}
	}
	users, err := h.repo.ListSCIMUsers(r.Context(), filter)
	if err != nil {
		return scimpkg.Page{}, err
	}
	resources := make([]scimpkg.Resource, 0, len(users))
	for _, user := range users {
		resources = append(resources, userResource(user.Principal, user.ExternalID))
	}
	return page(params, resources), nil
}

func (h userHandler) Replace(r *http.Request, id string, attrs scimpkg.ResourceAttributes) (scimpkg.Resource, error) {
	input := scimUserInput(id, attrs)
	var user access.SCIMUser
	err := runAuditedMutation(r, h.repo, func(txRepo Repository) (access.AuditEventInput, error) {
		var mutationErr error
		user, mutationErr = txRepo.UpsertSCIMUser(r.Context(), input)
		return scimAuditInput(r, "scim.user.update", "principal", user.Principal.ID, "success", nil), mutationErr
	})
	if err != nil {
		recordAudit(r.Context(), h.repo, r, "scim.user.update", "principal", id, "error", err)
		return scimpkg.Resource{}, err
	}
	return userResource(user.Principal, user.ExternalID), nil
}

func (h userHandler) Delete(r *http.Request, id string) error {
	var user access.SCIMUser
	err := runAuditedMutation(r, h.repo, func(txRepo Repository) (access.AuditEventInput, error) {
		var mutationErr error
		user, mutationErr = txRepo.DisableSCIMUser(r.Context(), id)
		return scimAuditInput(r, "scim.user.delete", "principal", user.Principal.ID, "success", nil), mutationErr
	})
	if err != nil {
		recordAudit(r.Context(), h.repo, r, "scim.user.delete", "principal", id, "error", err)
		if errors.Is(err, sql.ErrNoRows) {
			return scimerrors.ScimErrorResourceNotFound(id)
		}
		return err
	}
	return nil
}

func (h userHandler) Patch(r *http.Request, id string, ops []scimpkg.PatchOperation) (scimpkg.Resource, error) {
	current, err := h.Get(r, id)
	if err != nil {
		return scimpkg.Resource{}, err
	}
	attrs := cloneAttrs(current.Attributes)
	for _, op := range ops {
		applyUserPatch(attrs, op)
	}
	input := scimUserInput(id, attrs)
	action := "scim.user.update"
	if !input.Active {
		action = "scim.user.disable"
	}
	var user access.SCIMUser
	err = runAuditedMutation(r, h.repo, func(txRepo Repository) (access.AuditEventInput, error) {
		var mutationErr error
		user, mutationErr = txRepo.UpsertSCIMUser(r.Context(), input)
		return scimAuditInput(r, action, "principal", user.Principal.ID, "success", nil), mutationErr
	})
	if err != nil {
		recordAudit(r.Context(), h.repo, r, "scim.user.update", "principal", id, "error", err)
		return scimpkg.Resource{}, err
	}
	return userResource(user.Principal, user.ExternalID), nil
}

type groupHandler struct {
	repo Repository
}

func (h groupHandler) Create(r *http.Request, attrs scimpkg.ResourceAttributes) (scimpkg.Resource, error) {
	input := scimGroupInput("", attrs)
	var group access.Group
	err := runAuditedMutation(r, h.repo, func(txRepo Repository) (access.AuditEventInput, error) {
		var mutationErr error
		group, mutationErr = txRepo.UpsertSCIMGroup(r.Context(), input)
		return scimAuditInput(r, "scim.group.create", "group", group.ID, "success", nil), mutationErr
	})
	if err != nil {
		recordAudit(r.Context(), h.repo, r, "scim.group.create", "group", input.ExternalID, "error", err)
		return scimpkg.Resource{}, err
	}
	return h.groupResource(r.Context(), group, input.ExternalID)
}

func (h groupHandler) Get(r *http.Request, id string) (scimpkg.Resource, error) {
	groups, err := h.repo.ListSCIMGroups(r.Context(), access.SCIMGroupFilter{ID: id})
	if err != nil {
		return scimpkg.Resource{}, err
	}
	if len(groups) == 0 {
		return scimpkg.Resource{}, scimerrors.ScimErrorResourceNotFound(id)
	}
	return h.groupResource(r.Context(), groups[0], "")
}

func (h groupHandler) GetAll(r *http.Request, params scimpkg.ListRequestParams) (scimpkg.Page, error) {
	filter := access.SCIMGroupFilter{}
	if attr, value, ok := eqFilter(params); ok {
		switch strings.ToLower(attr) {
		case "displayname":
			filter.DisplayName = value
		case "externalid":
			filter.ExternalID = value
		}
	}
	groups, err := h.repo.ListSCIMGroups(r.Context(), filter)
	if err != nil {
		return scimpkg.Page{}, err
	}
	resources := make([]scimpkg.Resource, 0, len(groups))
	for _, group := range groups {
		resource, err := h.groupResource(r.Context(), group, "")
		if err != nil {
			return scimpkg.Page{}, err
		}
		resources = append(resources, resource)
	}
	return page(params, resources), nil
}

func (h groupHandler) Replace(r *http.Request, id string, attrs scimpkg.ResourceAttributes) (scimpkg.Resource, error) {
	input := scimGroupInput(id, attrs)
	var group access.Group
	err := runAuditedMutation(r, h.repo, func(txRepo Repository) (access.AuditEventInput, error) {
		var mutationErr error
		group, mutationErr = txRepo.UpsertSCIMGroup(r.Context(), input)
		return scimAuditInput(r, "scim.group.update", "group", group.ID, "success", nil), mutationErr
	})
	if err != nil {
		recordAudit(r.Context(), h.repo, r, "scim.group.update", "group", id, "error", err)
		return scimpkg.Resource{}, err
	}
	return h.groupResource(r.Context(), group, input.ExternalID)
}

func (h groupHandler) Delete(r *http.Request, id string) error {
	err := runAuditedMutation(r, h.repo, func(txRepo Repository) (access.AuditEventInput, error) {
		mutationErr := txRepo.DeleteSCIMGroup(r.Context(), id)
		return scimAuditInput(r, "scim.group.delete", "group", id, "success", nil), mutationErr
	})
	if err != nil {
		recordAudit(r.Context(), h.repo, r, "scim.group.delete", "group", id, "error", err)
		return err
	}
	return nil
}

func (h groupHandler) Patch(r *http.Request, id string, ops []scimpkg.PatchOperation) (scimpkg.Resource, error) {
	current, err := h.Get(r, id)
	if err != nil {
		return scimpkg.Resource{}, err
	}
	displayName := stringAttr(current.Attributes, "displayName")
	externalID := ""
	if current.ExternalID.Present() {
		externalID = current.ExternalID.Value()
	}
	memberSet := map[string]bool{}
	for _, memberID := range memberIDs(current.Attributes["members"]) {
		memberSet[memberID] = true
	}
	membersChanged := false
	memberAudits := []string{}
	for _, op := range ops {
		path := patchPath(op)
		switch strings.ToLower(op.Op) {
		case scimpkg.PatchOperationAdd:
			if path == "" {
				if m, ok := op.Value.(map[string]interface{}); ok {
					if v := stringAttr(m, "displayName"); v != "" {
						displayName = v
					}
					for _, memberID := range memberIDs(m["members"]) {
						if !memberSet[memberID] {
							memberSet[memberID] = true
							membersChanged = true
							memberAudits = append(memberAudits, "scim.group.member.add")
						}
					}
					continue
				}
			}
			if strings.EqualFold(path, "members") {
				for _, memberID := range memberIDs(op.Value) {
					if !memberSet[memberID] {
						memberSet[memberID] = true
						membersChanged = true
						memberAudits = append(memberAudits, "scim.group.member.add")
					}
				}
			}
		case scimpkg.PatchOperationRemove:
			if memberID := memberIDFromPath(path); memberID != "" {
				if memberSet[memberID] {
					delete(memberSet, memberID)
					membersChanged = true
					memberAudits = append(memberAudits, "scim.group.member.remove")
				}
				continue
			}
			if strings.EqualFold(path, "members") {
				if len(memberSet) > 0 {
					memberSet = map[string]bool{}
					membersChanged = true
					memberAudits = append(memberAudits, "scim.group.member.replace")
				}
				continue
			}
			if path == "" {
				for _, memberID := range memberIDs(op.Value) {
					if memberSet[memberID] {
						delete(memberSet, memberID)
						membersChanged = true
						memberAudits = append(memberAudits, "scim.group.member.remove")
					}
				}
			}
		case scimpkg.PatchOperationReplace:
			if strings.EqualFold(path, "displayName") {
				displayName = fmt.Sprint(op.Value)
				continue
			}
			if path == "" {
				if m, ok := op.Value.(map[string]interface{}); ok {
					if v := stringAttr(m, "displayName"); v != "" {
						displayName = v
					}
					if _, ok := m["members"]; ok {
						memberSet = memberSetFromIDs(memberIDs(m["members"]))
						membersChanged = true
						memberAudits = append(memberAudits, "scim.group.member.replace")
					}
					continue
				}
			}
			if strings.EqualFold(path, "members") {
				memberSet = memberSetFromIDs(memberIDs(op.Value))
				membersChanged = true
				memberAudits = append(memberAudits, "scim.group.member.replace")
			}
		}
	}
	var memberList []string
	if membersChanged {
		memberList = make([]string, 0, len(memberSet))
		for memberID := range memberSet {
			memberList = append(memberList, memberID)
		}
	}
	var group access.Group
	err = runAuditedMutationBatch(r, h.repo, func(txRepo Repository) ([]access.AuditEventInput, error) {
		var mutationErr error
		group, mutationErr = txRepo.UpsertSCIMGroup(r.Context(), access.SCIMGroupInput{ID: id, ExternalID: externalID, Name: displayName, MemberIDs: memberList})
		if mutationErr != nil {
			return nil, mutationErr
		}
		events := make([]access.AuditEventInput, 0, len(memberAudits)+1)
		for _, action := range memberAudits {
			events = append(events, scimAuditInput(r, action, "group", group.ID, "success", nil))
		}
		events = append(events, scimAuditInput(r, "scim.group.update", "group", group.ID, "success", nil))
		return events, nil
	})
	if err != nil {
		recordAudit(r.Context(), h.repo, r, "scim.group.update", "group", id, "error", err)
		return scimpkg.Resource{}, err
	}
	return h.groupResource(r.Context(), group, "")
}

func (h groupHandler) groupResource(ctx context.Context, group access.Group, externalID string) (scimpkg.Resource, error) {
	members, err := h.repo.ListSCIMGroupMembers(ctx, group.ID)
	if err != nil {
		return scimpkg.Resource{}, err
	}
	memberAttrs := make([]interface{}, 0, len(members))
	for _, member := range members {
		memberAttrs = append(memberAttrs, map[string]interface{}{
			"value":   member.PrincipalID,
			"display": firstNonEmpty(member.DisplayName, member.Email, member.PrincipalID),
		})
	}
	if externalID == "" {
		externalID = group.ExternalID
	}
	resource := scimpkg.Resource{
		ID: group.ID,
		Attributes: scimpkg.ResourceAttributes{
			"displayName": group.Name,
			"members":     memberAttrs,
		},
		Meta: createdMeta(group.CreatedAt),
	}
	if externalID != "" {
		resource.ExternalID = optional.NewString(externalID)
	}
	return resource, nil
}

func scimUserInput(id string, attrs scimpkg.ResourceAttributes) access.SCIMUserInput {
	email := primaryEmail(attrs)
	userName := stringAttr(attrs, "userName")
	displayName := firstNonEmpty(stringAttr(attrs, "displayName"), formattedName(attrs), email, userName)
	active := true
	if value, ok := attrs["active"].(bool); ok {
		active = value
	}
	return access.SCIMUserInput{
		ID:          strings.TrimSpace(id),
		ExternalID:  stringAttr(attrs, "externalId"),
		UserName:    userName,
		Email:       email,
		DisplayName: displayName,
		Active:      active,
	}
}

func scimGroupInput(id string, attrs scimpkg.ResourceAttributes) access.SCIMGroupInput {
	return access.SCIMGroupInput{
		ID:         strings.TrimSpace(id),
		ExternalID: stringAttr(attrs, "externalId"),
		Name:       stringAttr(attrs, "displayName"),
		MemberIDs:  memberIDs(attrs["members"]),
	}
}

func userResource(principal access.Principal, externalID string) scimpkg.Resource {
	active := principal.DisabledAt == ""
	resource := scimpkg.Resource{
		ID: principal.ID,
		Attributes: scimpkg.ResourceAttributes{
			"userName":    firstNonEmpty(principal.Email, principal.ID),
			"displayName": principal.DisplayName,
			"active":      active,
			"name": map[string]interface{}{
				"formatted": principal.DisplayName,
			},
			"emails": []interface{}{map[string]interface{}{
				"value":   principal.Email,
				"type":    "work",
				"primary": true,
			}},
		},
		Meta: createdMeta(principal.CreatedAt),
	}
	if externalID != "" {
		resource.ExternalID = optional.NewString(externalID)
	}
	return resource
}

func createdMeta(created string) scimpkg.Meta {
	var parsed time.Time
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		var err error
		parsed, err = time.Parse(layout, created)
		if err == nil {
			return scimpkg.Meta{Created: &parsed, LastModified: &parsed}
		}
	}
	return scimpkg.Meta{}
}

func eqFilter(params scimpkg.ListRequestParams) (string, string, bool) {
	if params.FilterValidator == nil {
		return "", "", false
	}
	expr, ok := params.FilterValidator.GetFilter().(*parserfilter.AttributeExpression)
	if !ok || expr.Operator != parserfilter.EQ {
		return "", "", false
	}
	value, ok := expr.CompareValue.(string)
	if !ok {
		return "", "", false
	}
	return expr.AttributePath.AttributeName, value, true
}

func page(params scimpkg.ListRequestParams, resources []scimpkg.Resource) scimpkg.Page {
	total := len(resources)
	start := params.StartIndex
	if start < 1 {
		start = 1
	}
	count := params.Count
	if count < 0 {
		count = 0
	}
	if count == 0 {
		return scimpkg.Page{TotalResults: total, Resources: []scimpkg.Resource{}}
	}
	from := start - 1
	if from > total {
		return scimpkg.Page{TotalResults: total, Resources: []scimpkg.Resource{}}
	}
	to := from + count
	if to > total {
		to = total
	}
	return scimpkg.Page{TotalResults: total, Resources: resources[from:to]}
}

func applyUserPatch(attrs scimpkg.ResourceAttributes, op scimpkg.PatchOperation) {
	path := patchPath(op)
	if path == "" {
		if values, ok := op.Value.(map[string]interface{}); ok {
			for key, value := range values {
				setUserAttr(attrs, key, value)
			}
		}
		return
	}
	if strings.EqualFold(op.Op, scimpkg.PatchOperationRemove) {
		removeUserAttr(attrs, path)
		return
	}
	setUserAttr(attrs, path, op.Value)
}

func cloneAttrs(attrs scimpkg.ResourceAttributes) scimpkg.ResourceAttributes {
	out := make(scimpkg.ResourceAttributes, len(attrs))
	for key, value := range attrs {
		switch typed := value.(type) {
		case map[string]interface{}:
			out[key] = cloneMap(typed)
		case []interface{}:
			out[key] = append([]interface{}(nil), typed...)
		default:
			out[key] = value
		}
	}
	return out
}

func cloneMap(values map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func setUserAttr(attrs scimpkg.ResourceAttributes, path string, value interface{}) {
	switch strings.ToLower(strings.TrimSpace(path)) {
	case "name.formatted":
		name, _ := attrs["name"].(map[string]interface{})
		if name == nil {
			name = map[string]interface{}{}
		}
		name["formatted"] = value
		attrs["name"] = name
	case "displayname":
		attrs["displayName"] = value
	case "username":
		attrs["userName"] = value
	case "active":
		attrs["active"] = value
	case "emails":
		attrs["emails"] = normalizeEmails(value)
	case "externalid":
		attrs["externalId"] = value
	default:
		attrs[path] = value
	}
}

func removeUserAttr(attrs scimpkg.ResourceAttributes, path string) {
	switch strings.ToLower(strings.TrimSpace(path)) {
	case "name.formatted":
		if name, ok := attrs["name"].(map[string]interface{}); ok {
			delete(name, "formatted")
		}
	case "displayname":
		delete(attrs, "displayName")
	case "username":
		delete(attrs, "userName")
	case "active":
		delete(attrs, "active")
	case "emails":
		delete(attrs, "emails")
	case "externalid":
		delete(attrs, "externalId")
	default:
		delete(attrs, path)
	}
}

func normalizeEmails(value interface{}) []interface{} {
	switch typed := value.(type) {
	case []interface{}:
		return typed
	case []map[string]any:
		out := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			out = append(out, map[string]interface{}(item))
		}
		return out
	case map[string]interface{}:
		return []interface{}{typed}
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []interface{}{map[string]interface{}{"value": typed, "type": "work", "primary": true}}
	default:
		return nil
	}
}

func patchPath(op scimpkg.PatchOperation) string {
	if op.Path == nil {
		return ""
	}
	return op.Path.String()
}

func stringAttr(attrs map[string]interface{}, key string) string {
	if value, ok := attrs[key]; ok {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return ""
}

func formattedName(attrs scimpkg.ResourceAttributes) string {
	if raw, ok := attrs["name"].(map[string]interface{}); ok {
		return stringAttr(raw, "formatted")
	}
	return ""
}

func primaryEmail(attrs scimpkg.ResourceAttributes) string {
	raw, ok := attrs["emails"].([]interface{})
	if !ok || len(raw) == 0 {
		return ""
	}
	first := ""
	for _, item := range raw {
		email, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		value := stringAttr(email, "value")
		if first == "" {
			first = value
		}
		if primary, _ := email["primary"].(bool); primary && value != "" {
			return value
		}
	}
	return first
}

func memberIDs(raw interface{}) []string {
	items, ok := raw.([]interface{})
	if !ok {
		if item, ok := raw.(map[string]interface{}); ok {
			items = []interface{}{item}
		} else {
			return nil
		}
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		member, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if id := stringAttr(member, "value"); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func memberSetFromIDs(ids []string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	return out
}

var memberPathRE = regexp.MustCompile(`(?i)^members\[value eq "([^"]+)"\]$`)

func memberIDFromPath(path string) string {
	match := memberPathRE.FindStringSubmatch(path)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func recordAudit(ctx context.Context, repo Repository, r *http.Request, action, targetType, targetID, status string, cause error) {
	if repo == nil {
		return
	}
	_ = access.PersistAuditEvent(ctx, repo, scimAuditInput(r, action, targetType, targetID, status, cause))
}

func scimAuditInput(r *http.Request, action, targetType, targetID, status string, cause error) access.AuditEventInput {
	metadata := map[string]any{
		"actor":  "scim",
		"path":   r.URL.Path,
		"method": r.Method,
	}
	if cause != nil {
		metadata["error"] = cause.Error()
	}
	bytes, _ := json.Marshal(metadata)
	return access.AuditEventInput{
		Action:        action,
		ResourceKind:  targetType,
		ResourceID:    targetID,
		Status:        status,
		RequestID:     requestIDFromRequest(r),
		CorrelationID: correlationIDFromRequest(r),
		MetadataJSON:  string(bytes),
	}
}

func runAuditedMutation(r *http.Request, repo Repository, mutation func(Repository) (access.AuditEventInput, error)) error {
	if transactional, ok := repo.(access.AuditedMutationRepository); ok {
		return transactional.RunAuditedMutation(r.Context(), func(txAccessRepo access.Repository) (access.AuditEventInput, error) {
			txRepo, ok := txAccessRepo.(Repository)
			if !ok {
				return access.AuditEventInput{}, fmt.Errorf("transactional access repository does not support SCIM")
			}
			return mutation(txRepo)
		})
	}
	input, err := mutation(repo)
	if err != nil {
		return err
	}
	return access.PersistAuditEvent(r.Context(), repo, input)
}

func runAuditedMutationBatch(r *http.Request, repo Repository, mutation func(Repository) ([]access.AuditEventInput, error)) error {
	if transactional, ok := repo.(access.AuditedMutationBatchRepository); ok {
		return transactional.RunAuditedMutationBatch(r.Context(), func(txAccessRepo access.Repository) ([]access.AuditEventInput, error) {
			txRepo, ok := txAccessRepo.(Repository)
			if !ok {
				return nil, fmt.Errorf("transactional access repository does not support SCIM")
			}
			return mutation(txRepo)
		})
	}
	inputs, err := mutation(repo)
	if err != nil {
		return err
	}
	for _, input := range inputs {
		if err := access.PersistAuditEvent(r.Context(), repo, input); err != nil {
			return err
		}
	}
	return nil
}

func requestIDFromRequest(r *http.Request) string {
	return firstNonEmpty(r.Header.Get("X-Request-Id"), r.Header.Get("X-Request-ID"))
}

func correlationIDFromRequest(r *http.Request) string {
	return firstNonEmpty(r.Header.Get("X-Correlation-Id"), r.Header.Get("X-Correlation-ID"), requestIDFromRequest(r))
}
