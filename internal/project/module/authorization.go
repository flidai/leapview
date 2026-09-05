package module

import (
	"encoding/json"
	"fmt"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

// CompileAuthorizationSnapshotJSON exposes Project's immutable manifest
// policy compiler through its module surface. Application composition passes
// only the persisted JSON contract and never imports Project internals.
func CompileAuthorizationSnapshotJSON(identity projectgraph.ServingIdentity, graph projectgraph.ProjectGraph, policyJSON string) (accesssnapshot.AuthorizationSnapshot, error) {
	var policy projectmanifest.AccessPolicy
	if err := json.Unmarshal([]byte(policyJSON), &policy); err != nil {
		return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("decode authorization policy: %w", err)
	}
	return projectmanifest.CompileAuthorizationSnapshot(identity, graph, policy)
}
