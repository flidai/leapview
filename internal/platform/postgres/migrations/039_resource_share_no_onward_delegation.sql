-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Ordinary resource shares are never delegation parents. Keep this invariant
-- at the database boundary as well as in the domain/service layers so a
-- repository caller cannot persist an onward-delegating share.
ALTER TABLE access.resource_share_grant
    ADD CONSTRAINT resource_share_grant_no_onward_delegation
    CHECK (allow_onward_delegation = false);

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- This security invariant is forward-only. Removing it would make historical
-- and newly issued resource shares capable of becoming delegation parents.
DO $$
BEGIN
    RAISE EXCEPTION 'resource share onward-delegation invariant is immutable';
END $$;
RESET ROLE;
