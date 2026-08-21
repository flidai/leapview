package http

import (
	"database/sql"
	stdhttp "net/http"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/go-chi/chi/v5"
)

func (h Handler) ListGroups(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListGroups(r.Context())
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, groupDTO(row))
	}
	_ = writePagedJSON(w, r, items)
}
func (h Handler) CreateGroup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input struct{ Name, DisplayName string }
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	var row access.Group
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		row, mutationErr = tx.UpsertGroup(r.Context(), access.GroupInput{Provider: "local", ExternalID: input.Name, Name: firstNonEmpty(input.DisplayName, input.Name)})
		return auditInput(r, "group.created", h.currentPrincipalID(r), "group", row.ID, "", "success", groupAuditMetadata(row)), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreateGroup(), err, stdhttp.StatusBadRequest)
		return
	}
	if revision, revisionErr := access.GroupRevision(row); revisionErr == nil {
		w.Header().Set("ETag", revision)
	}
	writeJSON(w, stdhttp.StatusCreated, groupDTO(row))
}
func (h Handler) GetGroup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	row, err := findGroup(r.Context(), repo, chi.URLParam(r, "group"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if revision, revisionErr := access.GroupRevision(row); revisionErr == nil {
		w.Header().Set("ETag", revision)
	}
	writeJSON(w, stdhttp.StatusOK, groupDTO(row))
}
func (h Handler) UpdateGroup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input struct {
		DisplayName string `json:"displayName"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	row, err := findGroup(r.Context(), repo, chi.URLParam(r, "group"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if !groupIsLocallyManaged(row) {
		writeJSONError(w, groupManagedExternallyError(row), stdhttp.StatusUnprocessableEntity)
		return
	}
	if revision, revisionErr := access.GroupRevision(row); revisionErr == nil {
		w.Header().Set("ETag", revision)
	}
	original := row
	var currentGroup access.Group
	err = runAuditedMutationWithRevision(r, repo, func(tx access.Repository) (string, error) {
		rows, err := tx.ListGroups(r.Context())
		if err != nil {
			return "", err
		}
		for _, current := range rows {
			if current.ID == original.ID {
				currentGroup = current
				return access.GroupRevision(current)
			}
		}
		return "", sql.ErrNoRows
	}, func(tx access.Repository) (access.AuditEventInput, error) {
		if !groupIsLocallyManaged(currentGroup) {
			return access.AuditEventInput{}, groupManagedExternallyError(currentGroup)
		}
		var mutationErr error
		row, mutationErr = tx.UpsertGroup(r.Context(), access.GroupInput{ID: currentGroup.ID, Provider: currentGroup.Provider, ExternalID: currentGroup.ExternalID, Name: firstNonEmpty(input.DisplayName, currentGroup.Name)})
		return auditInput(r, "group.updated", h.currentPrincipalID(r), "group", row.ID, "", "success", groupAuditMetadata(row)), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationUpdateGroup(), err, stdhttp.StatusBadRequest)
		return
	}
	if revision, revisionErr := access.GroupRevision(row); revisionErr == nil {
		w.Header().Set("ETag", revision)
	}
	writeJSON(w, stdhttp.StatusOK, groupDTO(row))
}
func (h Handler) DeleteGroup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	group, err := findGroup(r.Context(), repo, chi.URLParam(r, "group"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if !groupIsLocallyManaged(group) {
		writeJSONError(w, groupManagedExternallyError(group), stdhttp.StatusUnprocessableEntity)
		return
	}
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		mutationErr := tx.DeleteGroup(r.Context(), group.ID)
		return auditInput(r, "group.deleted", h.currentPrincipalID(r), "group", group.ID, "", "success", groupAuditMetadata(group)), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationDeleteGroup(), err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}
func (h Handler) ListGroupMembers(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListGroupMembers(r.Context(), chi.URLParam(r, "group"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, groupMemberPrincipalDTO(row))
	}
	_ = writePagedJSON(w, r, items)
}
func (h Handler) AddGroupMember(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	group, err := findGroup(r.Context(), repo, chi.URLParam(r, "group"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if !groupIsLocallyManaged(group) {
		writeJSONError(w, groupManagedExternallyError(group), stdhttp.StatusUnprocessableEntity)
		return
	}
	principalID := chi.URLParam(r, "principal")
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		mutationErr := tx.AddGroupMember(r.Context(), group.ID, principalID)
		return auditInput(r, "group.member_added", h.currentPrincipalID(r), "group_member", group.ID+":"+principalID, "", "success", map[string]any{"groupId": group.ID, "memberPrincipalId": principalID}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationAddGroupMember(), err, statusForNotFound(err))
		return
	}
	writeJSON(w, stdhttp.StatusOK, map[string]string{"status": "added"})
}
func (h Handler) RemoveGroupMember(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	group, err := findGroup(r.Context(), repo, chi.URLParam(r, "group"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if !groupIsLocallyManaged(group) {
		writeJSONError(w, groupManagedExternallyError(group), stdhttp.StatusUnprocessableEntity)
		return
	}
	principalID := chi.URLParam(r, "principal")
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		mutationErr := tx.RemoveGroupMember(r.Context(), group.ID, principalID)
		return auditInput(r, "group.member_removed", h.currentPrincipalID(r), "group_member", group.ID+":"+principalID, "", "success", map[string]any{"groupId": group.ID, "memberPrincipalId": principalID}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationRemoveGroupMember(), err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}
