-- Bounded access operational retention.  The owner function is the only
-- destructive surface exposed to the separately authenticated maintenance
-- role; runtime repositories use the regular auth queries and never call it.

-- name: PruneAuthState :one
SELECT result.requested_cutoff::timestamptz AS requested_cutoff,
       result.cutoff::timestamptz AS cutoff,
       result.requested_limit::integer AS requested_limit,
       result.sessions_removed::bigint AS sessions_removed,
       result.oauth_sessions_removed::bigint AS oauth_sessions_removed,
       result.oauth_assertions_removed::bigint AS oauth_assertions_removed,
       result.desktop_codes_removed::bigint AS desktop_codes_removed,
       result.device_authorizations_removed::bigint AS device_authorizations_removed,
       result.api_tokens_removed::bigint AS api_tokens_removed,
       result.service_secrets_removed::bigint AS service_secrets_removed,
       result.authoring_sessions_removed::bigint AS authoring_sessions_removed,
       result.authoring_credentials_removed::bigint AS authoring_credentials_removed,
       result.auth_state_floor::timestamptz AS auth_state_floor
FROM access.prune_auth_state(sqlc.arg(requested_cutoff), sqlc.arg(batch_limit))
     AS result(requested_cutoff, cutoff, requested_limit,
               sessions_removed, oauth_sessions_removed,
               oauth_assertions_removed, desktop_codes_removed,
               device_authorizations_removed, api_tokens_removed,
               service_secrets_removed, authoring_sessions_removed,
               authoring_credentials_removed, auth_state_floor);
