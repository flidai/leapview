-- +goose Up
-- +goose StatementBegin

-- Persist the authenticated plan actor as part of durable delivery control
-- state. Older rows recover the same actor recorded by the append-only
-- plan-created event; the fallback is only for pre-ledger fixtures.
ALTER TABLE delivery_plans ADD COLUMN actor_id TEXT NOT NULL DEFAULT 'delivery'
  CHECK (length(actor_id) BETWEEN 1 AND 128
    AND actor_id = trim(actor_id)
    AND actor_id NOT GLOB '*[^A-Za-z0-9._:/-]*');

UPDATE delivery_plans
SET actor_id = COALESCE(
  (SELECT events.actor_id
   FROM delivery_events events
   WHERE events.object_kind = 'plan'
     AND events.object_id = delivery_plans.id
     AND events.event_kind = 'plan_created'
   ORDER BY events.created_at ASC, events.id ASC
   LIMIT 1),
  actor_id
);

-- +goose StatementEnd

-- +goose Down

-- Forward-only: the actor is durable command and audit evidence.
SELECT 1;
