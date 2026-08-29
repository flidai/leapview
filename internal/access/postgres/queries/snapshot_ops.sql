-- Snapshot activation leaves not shared with the general extended access
-- repository. Transaction ownership and immutable replay checks remain in
-- snapshot.go.

-- name: UpsertDashboardPublicationPrincipal :exec
INSERT INTO access.principal(id, principal_type, status, email, display_name)
VALUES (sqlc.arg(id)::uuid, 'dashboard_publication', 'active', '', sqlc.arg(name))
ON CONFLICT (id) DO UPDATE
SET display_name = EXCLUDED.display_name,
    updated_at = clock_timestamp();
